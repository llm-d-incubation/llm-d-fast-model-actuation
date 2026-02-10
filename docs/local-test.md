# Local testing with `kind`

```shell
kind delete cluster --name fmatest
kind create cluster --name fmatest

kubectl create clusterrole node-viewer --verb=get,list,watch --resource=nodes

kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: testreq
rules:
- apiGroups:
  - ""
  resourceNames:
  - gpu-map
  - gpu-allocs
  resources:
  - configmaps
  verbs:
  - update
  - patch
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - configmaps
  verbs:
  - create
EOF

kubectl create rolebinding testreq --role=testreq --serviceaccount=$(kubectl get sa default -o jsonpath={.metadata.namespace}):testreq

kubectl create sa testreq
kubectl create cm gpu-map
nl=$'\n'
let gi=0
kubectl get nodes -o name | sed 's%^node/%%' | while read node; do
    kubectl label node $node nvidia.com/gpu.present=true nvidia.com/gpu.product=NVIDIA-L40S nvidia.com/gpu.count=2 --overwrite=true
    kubectl patch node $node --subresource status -p '{"status": {"capacity": {"nvidia.com/gpu": 2}, "allocatable": {"nvidia.com/gpu": 2} }}'
    let gi1=gi+1
    kubectl patch cm gpu-map -p "data:${nl} ${node}: '{\"GPU-$gi\": 0, \"GPU-$gi1\": 1 }'"
    let gi=gi1+1
done
```

```shell
make build-test-requester-local
make build-test-server-local
make build-controller-local

tag=$(git rev-parse --short HEAD)
regy=my-registry/my-namespace

kind load docker-image $regy/test-requester:$tag          --name fmatest
kind load docker-image $regy/test-server:$tag             --name fmatest
kind load docker-image $regy/dual-pods-controller:$tag    --name fmatest

helm upgrade --install dpctlr charts/dual-pods-controller --set Image="$regy/dual-pods-controller:$tag" --set NodeViewClusterRole=node-viewer --set SleeperLimit=1 --set Local=true

function mkrs() {
inst=$(date +%H-%M-%S)
kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: my-request-$inst
  labels:
    app: dp-example
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dp-example
  template:
    metadata:
      labels:
        app: dp-example
        instance: "$inst"
      annotations:
        dual-pods.llm-d.ai/admin-port: "8081"
        dual-pods.llm-d.ai/server-patch: |
          metadata:
            labels: {
              "model-reg": "ibm-granite",
              "model-repo": "granite-3.3-2b-instruct",
              "app": null}
          spec:
            containers:
            - name: inference-server
              image: $regy/test-server:$tag
              command: [ /ko-app/test-server ]
              resources:
                limits:
                  cpu: "2"
                  memory: 9Gi
              readinessProbe:
                httpGet:
                  path: /health
                  port: 8000
                initialDelaySeconds: 10
                periodSeconds: 5
    spec:
      containers:
        - name: inference-server
          image: $regy/test-requester:$tag
          imagePullPolicy: Never
          command:
          - /ko-app/test-requester
          - --node=\$(NODE_NAME)
          - --pod-id=\$(POD_NAME)
          - --namespace=\$(NAMESPACE)
          env:
            - name: NODE_NAME
              valueFrom:
                fieldRef: { fieldPath: spec.nodeName }
            - name: POD_NAME
              valueFrom:
                fieldRef: { fieldPath: metadata.name }
            - name: NAMESPACE
              valueFrom:
                fieldRef: { fieldPath: metadata.namespace }
          ports:
          - name: probes
            containerPort: 8080
          - name: spi
            containerPort: 8081
          readinessProbe:
            httpGet:
              path: /ready
              port: 8080
            initialDelaySeconds: 2
            periodSeconds: 5
          resources:
            limits:
              nvidia.com/gpu: "1"
              cpu: "1"
              memory: 250Mi
      serviceAccount: testreq
EOF
}
```
