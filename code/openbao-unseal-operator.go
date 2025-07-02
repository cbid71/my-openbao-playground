package main

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var log = logf.Log.WithName("openbao-unsealer")

const (
	// Label pour identifier les pods OpenBAO
	OpenBAOLabel = "app.kubernetes.io/name"
	OpenBAOValue = "openbao"
	
	// Annotation pour marquer les pods comme unsealés
	UnsealedAnnotation = "openbao.io/unsealed"
	
	// Port par défaut d'OpenBAO
	DefaultBAOPort = 8200
)

type OpenBAOReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Clientset  *kubernetes.Clientset
	RestConfig *rest.Config
}

func (r *OpenBAOReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.WithValues("pod", req.NamespacedName)
	
	// Récupérer le pod
	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		log.Error(err, "unable to fetch Pod")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Vérifier si c'est un pod OpenBAO
	if !r.isOpenBAOPod(&pod) {
		return ctrl.Result{}, nil
	}

	// Vérifier si déjà unsealé
	if r.isAlreadyUnsealed(&pod) {
		log.Info("Pod already unsealed, skipping")
		return ctrl.Result{}, nil
	}

	// Vérifier si le pod est prêt
	if !r.isPodReady(&pod) {
		log.Info("Pod not ready yet, requeuing")
		return ctrl.Result{RequeueAfter: time.Second * 10}, nil
	}

	// Unseal le pod
	if err := r.unsealPod(ctx, &pod); err != nil {
		log.Error(err, "failed to unseal pod")
		return ctrl.Result{RequeueAfter: time.Second * 30}, err
	}

	// Marquer comme unsealé
	if err := r.markAsUnsealed(ctx, &pod); err != nil {
		log.Error(err, "failed to mark pod as unsealed")
		return ctrl.Result{}, err
	}

	log.Info("Successfully unsealed OpenBAO pod")
	return ctrl.Result{}, nil
}

func (r *OpenBAOReconciler) isOpenBAOPod(pod *corev1.Pod) bool {
	if pod.Labels == nil {
		return false
	}
	
	// Vérifier le label standard
	if value, exists := pod.Labels[OpenBAOLabel]; exists && value == OpenBAOValue {
		return true
	}
	
	// Vérifier aussi si le nom du container contient "bao" ou "openbao"
	for _, container := range pod.Spec.Containers {
		if container.Name == "openbao" || container.Name == "bao" {
			return true
		}
	}
	
	return false
}

func (r *OpenBAOReconciler) isAlreadyUnsealed(pod *corev1.Pod) bool {
	if pod.Annotations == nil {
		return false
	}
	
	_, exists := pod.Annotations[UnsealedAnnotation]
	return exists
}

func (r *OpenBAOReconciler) isPodReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *OpenBAOReconciler) unsealPod(ctx context.Context, pod *corev1.Pod) error {
	log := log.WithValues("pod", pod.Name, "namespace", pod.Namespace)
	
	// Récupérer la clé d'unseal depuis le secret
	unsealKey, err := r.getUnsealKey(ctx, pod.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get unseal key: %w", err)
	}

	// Trouver le bon container OpenBAO
	containerName := r.findOpenBAOContainer(pod)
	if containerName == "" {
		return fmt.Errorf("no OpenBAO container found in pod")
	}

	// Construire la commande d'unseal
	cmd := []string{"bao", "unseal", unsealKey}
	
	log.Info("Executing unseal command", "container", containerName)
	
	// Exécuter la commande dans le pod
	if err := r.execInPod(pod.Namespace, pod.Name, containerName, cmd); err != nil {
		return fmt.Errorf("failed to execute unseal command: %w", err)
	}

	return nil
}

func (r *OpenBAOReconciler) getUnsealKey(ctx context.Context, namespace string) (string, error) {
	// Récupérer la clé depuis un secret Kubernetes
	var secret corev1.Secret
	secretName := types.NamespacedName{
		Name:      "openbao-unseal-key", // Nom du secret à adapter
		Namespace: namespace,
	}
	
	if err := r.Get(ctx, secretName, &secret); err != nil {
		return "", err
	}
	
	unsealKey, exists := secret.Data["unseal-key"]
	if !exists {
		return "", fmt.Errorf("unseal-key not found in secret")
	}
	
	return string(unsealKey), nil
}

func (r *OpenBAOReconciler) findOpenBAOContainer(pod *corev1.Pod) string {
	// Chercher le container OpenBAO
	for _, container := range pod.Spec.Containers {
		if container.Name == "openbao" || container.Name == "bao" {
			return container.Name
		}
	}
	
	// Si pas trouvé, utiliser le premier container
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name
	}
	
	return ""
}

func (r *OpenBAOReconciler) execInPod(namespace, podName, containerName string, cmd []string) error {
	req := r.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		Param("container", containerName)
	
	req.VersionedParams(&corev1.PodExecOptions{
		Container: containerName,
		Command:   cmd,
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(r.RestConfig, "POST", req.URL())
	if err != nil {
		return err
	}

	return exec.Stream(remotecommand.StreamOptions{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
}

func (r *OpenBAOReconciler) markAsUnsealed(ctx context.Context, pod *corev1.Pod) error {
	// Ajouter l'annotation pour marquer comme unsealé
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	
	pod.Annotations[UnsealedAnnotation] = time.Now().Format(time.RFC3339)
	
	return r.Update(ctx, pod)
}

func (r *OpenBAOReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Créer un prédicat pour filtrer les événements
	podPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			pod, ok := e.Object.(*corev1.Pod)
			if !ok {
				return false
			}
			return r.isOpenBAOPod(pod)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			pod, ok := e.ObjectNew.(*corev1.Pod)
			if !ok {
				return false
			}
			return r.isOpenBAOPod(pod) && !r.isAlreadyUnsealed(pod)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // On ne s'intéresse pas aux suppressions
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		WithEventFilter(podPredicate).
		Complete(r)
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme.Scheme,
	})
	if err != nil {
		log.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Créer le clientset pour l'exec
	clientset, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		log.Error(err, "unable to create clientset")
		os.Exit(1)
	}

	if err = (&OpenBAOReconciler{
		Client:     mgr.GetClient(),
		Scheme:     mgr.GetScheme(),
		Clientset:  clientset,
		RestConfig: mgr.GetConfig(),
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "unable to create controller", "controller", "OpenBAO")
		os.Exit(1)
	}

	log.Info("Starting OpenBAO Unseal Operator")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "problem running manager")
		os.Exit(1)
	}
}