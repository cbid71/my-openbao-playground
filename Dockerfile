FROM    golang:tip-bullseye
LABEL   authors="Cyrille BIARD https://github.com/cbid71"

WORKDIR /app

COPY code/openbao-unseal-operator.go /app/

RUN go mod init openbao-unseal-operator

RUN go get k8s.io/api/core/v1                                                                                                                                                       
RUN go get k8s.io/apimachinery/pkg/runtime                                                                                                                                          
RUN go get k8s.io/apimachinery/pkg/types
RUN go get k8s.io/client-go/kubernetes
RUN go get k8s.io/client-go/kubernetes/scheme
RUN go get k8s.io/client-go/rest
RUN go get k8s.io/client-go/tools/remotecommand
RUN go get sigs.k8s.io/controller-runtime
RUN go get sigs.k8s.io/controller-runtime/pkg/client
RUN go get sigs.k8s.io/controller-runtime/pkg/controller
RUN go get sigs.k8s.io/controller-runtime/pkg/event
RUN go get sigs.k8s.io/controller-runtime/pkg/log
RUN go get sigs.k8s.io/controller-runtime/pkg/log/zap
RUN go get sigs.k8s.io/controller-runtime/pkg/predicate



RUN go build /app/openbao-unseal-operator.go


