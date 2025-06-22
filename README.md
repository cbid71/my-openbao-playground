# Let's play with OpenBao

- Install it on kubernetes with injector enabled
- Prepare a data and store it on the openBAO
- create a pod able to fetch data and store it into a file on the pod.


## Installation

Prepare the DNS

```
IP_K3S=$(hostname -I | awk '{print $1}')
echo $IP_K3S my-openbao.local | sudo tee -a /etc/hosts
```

Install OpenBao

```
helm dependency build ./my-openbao/
kubectl create namespace play
helm upgrade -i my-openbao ./my-openbao/ -n play
```
Initialize OpenBAO
```
kubectl exec -n play -ti openbao-0 -- bao operator init

keyShares: 5
keyThreshold: 3

# Register the output (a json) as credentials.json
```

Unseal OpenBAO
```
kubectl exec -n play -ti my-openbao-0 -- bao operator unseal XXXXXXXXXXXXXXXXXX
kubectl exec -n play -ti my-openbao-0 -- bao operator unseal YYYYYYYYYYYYYYYY
kubectl exec -n play -ti my-openbao-0 -- bao operator unseal zzzzzzzzzzzz
```

Log in OpenBao and active `kubernetes` and `kv` engines
```
kubectl exec -n play -ti my-openbao-0 -- bao login
# note : the root token have been given through the `vault operator init` command.

kubectl exec -n play -ti my-openbao-0 -- bao secrets enable kubernetes
# note: can be done for a specific path too
# kubectl exec -n play -ti my-openbao-0 -- bao secrets enable -path=my-cluster kubernetes 

kubectl exec -n play -ti my-openbao-0 -- bao secrets enable kv
# per default, that enable the secret to be stored on a path starting with /kv/ we can change this
# kubectl exec -n play -ti my-openbao-0 -- bao secrets enable kv -path=v2
```

Prepare a very simple policy that we will use later
```
# prepare a policy, it will define the rules for what our app pods are allowed to do in openBAO 
kubectl -n play exec -i my-openbao-0 -- /bin/sh -c "cat > /home/openbao/app-policy.hcl" <<EOF
path "kv/secret/myapp/*" {
  capabilities = ["read"]
}
EOF
# note : this policy is very open, and only for test
kubectl -n play exec -i my-openbao-0 -- bao policy write pod-read-only-policy /home/openbao/app-policy.hcl
```

Prepare a specific service account, it will be used by openBAO the request the control plane.

``` sa-bao.yaml

---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: vault-auth
  namespace: play
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: vault-auth-role
rules:
  - apiGroups: [""]
    resources: ["serviceaccounts", "pods"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["list", "watch"]
  - apiGroups: ["authentication.k8s.io"]
    resources: ["tokenreviews"]
    verbs: ["create"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["subjectaccessreviews"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: vault-auth-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: vault-auth-role
subjects:
  - kind: ServiceAccount
    name: vault-auth
    namespace: play
---
apiVersion: v1
kind: Secret
metadata:
  name: vault-auth-token
  annotations:
    kubernetes.io/service-account.name: vault-auth
  namespace: play
type: kubernetes.io/service-account-token
```

```
kubectl apply -f sa-bao.yaml
```

Then make OpenBAO knowing this service account and associate it to kubernetes

```
# The following will make Openbao knowing that he can use this service account
# and it allows openbao to request the controlplane to get some informations about pods that will request openbao
#bao write auth/kubernetes/config \
#    token_reviewer_jwt="<your reviewer service account JWT>" \
#    kubernetes_host=https://192.168.99.100:<your TCP port or blank for 443> \
#    kubernetes_ca_cert=@/var/run/secrets/kubernetes.io/serviceaccount/ca.crt


TOKEN=$(kubectl get secret vault-auth-token -n play -o jsonpath="{.data.token}" | base64 -d)
CA_CERT=$(kubectl get secret vault-auth-token -n play -o jsonpath="{.data['ca\.crt']}" | base64 -d)

kubectl exec -n play -i my-openbao-0 -- /bin/sh -c "bao write auth/kubernetes/config \
  token_reviewer_jwt=\"$TOKEN\" \
  kubernetes_host=https://kubernetes.default.svc \
  kubernetes_ca_cert=\"$CA_CERT\""
```

Then you create an OpenBAO role, it associate a **future service account assumed by you app pods** to an OpenBAO policy.

```
# We create an openBao role for each kubernetes serviceaccount associated to pods that will try to request OpenBAO
# for the demo we can imagine that all our pods will use the same serviceaccount named "demo-sa" and we will create an openbao role 'demo-role'
# of course in a real world app, you can have several roles, one for each service account
# or have several roles associated to the same service account
# but PLEASE keep it simple : one role = one service account
# here the role 'demo-role' will associate the kubernetes service account 'demo-sa' to the openbao policy 'pod-read-only-policy'
 
kubectl -n play exec -i my-openbao-0 -- bao write auth/kubernetes/role/demo-role \
    bound_service_account_names=demo-sa \
    bound_service_account_namespaces=play \
    policies=pod-read-only-policy \
    ttl=24h
```

NOW we are ready to request and get secrets from OpenBAO since we already activated OpenBAO injector.


## Application using OpenBAO

We will deploy an application 

first we create a secret
```
kubectl -n play exec -i my-openbao-0 -- bao kv put kv/secret/myapp/config username=demo password=s3cr3t
kubectl -n play exec -i my-openbao-0 -- bao kv get kv/secret/myapp/config
kubectl -n play exec -i my-openbao-0 -- bao kv list kv/secret/myapp/
```

then we create our serviceaccount 
```demo-sa.yaml
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: demo-sa
  namespace: play
```
```
kubectl apply -f demo-sa.yaml
```

Then we finally create our pod containing our all fresh informations :

```demo-pod.yaml
---
apiVersion: v1
kind: Pod
metadata:
  name: demo-pod
  namespace: play
  annotations:
    vault.hashicorp.com/agent-inject: "true"
    vault.hashicorp.com/role: "demo-role"
    vault.hashicorp.com/agent-inject-secret-config.txt: "kv/secret/myapp/config"
    vault.hashicorp.com/agent-inject-template-config.txt: |
      {{- with secret "kv/secret/myapp/config" -}}
      USERNAME={{ .Data.username }}
      PASSWORD={{ .Data.password }}
      {{- end }}
spec:
  serviceAccountName: demo-sa
  containers:
    - name: app
      image: busybox
      command: ["sh", "-c", "cat /vault/secrets/config.txt && sleep infinity"]
```
```
kubectl apply -f demo-pod.yaml
```

**Explaination :**

the annotations will tell kubernetes to add a sidecar to the pod, in charge of fetching the secret.

`vault.hashicorp.com/agent-inject-secret-*.txt` must contains the real full path of the secret
`vault.hashicorp.com/agent-inject-template-*.txt` is a template that will translate the content of the secret into a file
By default the file will be mounted on the pod in `/vault/secrets/*.txt`
The defaut location of the file can be changed by adding an annotation `vault.hashicorp.com/agent-inject-path:` valuing a directory where everything we be stored
example:
`vault.hashicorp.com/agent-inject-path: "/my/custom/path"` 


To see the logs associated to the pod's sidecar in charge of fetching the secret :

```
kubectl -n <namespace> logs <pod name> -c vault-agent-init
kubectl -n play logs demo-pod -c vault-agent-init
```

# TODO

The sealing will reapply each time the pod is rescheduled, to avoid this problem openBAO propose to use "google kms auto unseal"
we probably should associate an auto-unseal 

https://openbao.org/docs/platform/k8s/helm/run/#google-kms-auto-unseal



