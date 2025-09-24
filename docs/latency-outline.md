# Latency Outline

Well, really it is another outline of the whole thing, but laser
focused on latency (since the whole point is good latency).

## Wake_up case

We have a roadmap of stages of development that is still not fully
agreed. For the sake of concreteness, I will focus here on one
proposed version of one stage of development. It is part of the
following scenario description.

- Time to first token, as measured by inference client (IC).

- Scale up from zero, with some not-entirely-clear-to-me pre-routing
  component that can queue requests. I am going to pretend that
  inference request queuing, dispatching, and routing are part of
  something called inference scheduling (IS). I am not sure whether
  everybody else means all these things when they use this term.

- Some auto-scaler (AS) that reacts to signals from the inference
  scheduler and other information and responds by updating a
  Prometheus metric that it offers and that stipulates the replica
  count for each model variant.

- Some replica count relay (RCR) that rapidly scrapes the
  aforementioned metric from the AS and syncs those values to the
  replica count of each variant's ReplicaSet. For concreteness and
  simplicity I suppose here that each variant is deployed as a
  ReplicaSet of server-requesting Pods.

- The dual-pod design described in
  https://github.com/lionelvillard/llm-d-fast-model-actuation/blob/7dc4f0b820d62018769284fe99c21542ca9f4720/docs/dual-pods.md
  (which is the current version of
  https://github.com/lionelvillard/llm-d-fast-model-actuation/pull/2,
  which itself is a proposed modificaion of
  https://github.com/llm-d-incubation/llm-d-fast-model-actuation/pull/15)
  as the "Sleep/wake only" stage of development.

- The inference request is for a variant with some sleeping instances
  and no awake instances.

- Client already has a TLS connection open to the Inference Gateway (IGW).

Now, on to the latency summary.

- Client sends inference request to the IGW.

- IGW receives request, parses it, and delegates it (i.e., sends a
  similar request) to the IS.

- IS receives the request, parses it, and realizes that the
  InferencePool for that request currently has zero available
  servers. IS starts some process of getting this "pool miss"
  interrupt to the AS.

- The pool miss interrupt travels from the IS to the AS.

- The AS does some level analysis and planning, updating its offered
  Prometheus metrics.

- The RR gets around to scraping the AS.

- The RR processes the latest metrics and consequently sends a request
  to a kube-apiserver to update the replica count of the relevant
  ReplicaSet.

- The kube-apiserver does its thing to process that request. This
  includes doing an etcd transaction. In an OpenShift cluster there
  are extra steps. The kube-apiserver eventually (the word
  "eventually" is here because this could take a while; but I should
  report that on a lightly loaded plain kube cluster it is not
  uncommon for this event to be received before the client gets the
  response to the request) sends an update event to the ReplicaSet
  controller, which has an
  [informer](https://github.com/kubernetes/client-go/blob/v0.34.1/tools/cache/shared_informer.go#L40-L237)
  watching the ReplicaSets.

- The ReplicaSet informer receives the update event and puts a
  reference to the ReplicaSet into the controller's work
  queue. Eventually a worker goroutine dequeues that reference and
  works on that ReplicaSet. Last time I looked, this involved doing a
  query to the kube-apiserver to get a current list of members of the
  set. The worker decides that there are not enough members in the
  set, and sends to the kube-apiserver a request to create another
  member (in our scenario, this is the server-requesting Pod).

- The kube-apiserver does its thing with this Pod creation
  request. This includes doing an etcd transaction. In an OpenShift
  cluster there are extra steps. The kube-apiserver eventually sends a
  create event to the kube-scheduler.

- The kube-scheduler does its thing --- which is very extensible. I am
  not sure of what to suppose here. In the best case this involves no
  interactions with other components. Eventually the scheduler decides
  which Node should run the new Pod and sends a request to the
  kube-apiserver to make that binding.

- The kube-apiserver does its thing with this Pod update request. This
  includes doing an etcd transaction. In an OpenShift cluster there
  are extra steps. The kube-apiserver eventually sends a Pod update
  event to the kubelet that will run the Pod.

- The kubelet receives the Pod update event and begins working on
  running the Pod. This includes stuff that depends on details about
  volumes and container images. Let us suppose the best case, in which
  the stub/requester container image is already on the node and there
  is no waiting on anything for volume creation/binding. The kubelet
  constructs the Pod, including the stub/requester container, and
  starts the main process inside that container. The kubelet sends a
  Pod update request to the kube-apiserver to update the status of the
  Pod to reflect that it has begun running.

- The kube-apiserver does its thing with this Pod update request. This
  includes doing an etcd transaction. In an OpenShift cluster there
  are extra steps. The kube-apiserver eventually sends a Pod update
  event to the dual-pod controller, which has an informer on
  server-requesting Pods.

- The dual-pod controller receives this event and enqueues a reference
  to the server-requesting Pod. Eventually a worker goroutine dequeues
  that reference and begins working on that Pod. The worker sends a
  request to the stub/requester to get the assigned GPUs.

- In the meantime, the stub/requester has started running and
  determined what GPUs are assigned to it. We are not guaranteed that
  this will be done before the (first) request for that set is
  received, but this is a plausible case to focus on first.

- The stub/requester receives the request for its assigned GPU set,
  already knows the answer, and sends the response.

- The dual-pod controller worker goroutne that sent the GPU assignment
  request receives the reply (here I suppose that this worker uses
  simple straight line code for this request/response interaction;
  that is not the only possible choice, but will analyze this approach
  first).

- That same goroutine determines that the server request can be
  answered by waking a sleeping vLLM instance, and sends that
  `/wake_up` request to the vLLM instance.

- The vLLM instance receives the `/wake_up` request and handles it,
  eventually sending a response.

- The dual-pod controller worker goroutine that sent the `/wake_up`
  request was waiting synchronously for the response (again, this is
  not the only possible choice, just the first one that I will
  analyze) and eventally receives it. This goroutine next sends the
  POST request to the stub/requester that conveys the URL to poll for
  inference server readiness.

- The stub/requester receives the POST request conveying the URL to
  poll for readiness. The stub/requester the sends the response, and
  then sends the readiness request to that URL.

- The vLLM instance receives that readiness request. In this case it
  is certainly ready, because `/wake_up` does not return until the
  instance is ready. The instance sends a positive response for the
  readiness query.

- The stub/requester receives the positive POST reply and records that
  the inference server is now ready.

- The kubelet running the server-requesting Pod eventually gets around
  to polling the stub/requester container for readiness. A positive
  response is sent.

- The kubelet receives the positive response and then sends to the
  kube-apiserver a request to update the server-requesting Pod's
  status.

- The kube-apiserver does its thing with this Pod update request from
  the kubelet (reporting readiness). This includes doing an etcd
  transactions. In an OpenShift cluster there are extra steps. The
  kube-apiserver eventually sends a Pod update event to the IS, which
  has been waiting for somewhere to send the inference request.

- Eventually the IS sees that the InferencePool now has a ready
  member, and delegates the inference request to that member (the vLLM
  instance).

- The vLLM instance receives the inference request and starts working
  on it. Eventually the first token is sent back to the IS.

- The IS receives the first token and returns it to the IGW.

- The IGW receives the first token and returns it to the inference
  client.

## Server create case

This is like the wake_up case except that there are no sleeping
replicas, but the model has been staged to the chosen Node and the
shared torch.compile cache is hit. We suppose that in order to make
GPU memory room for the new vLLM instance, a sleeping one will have to
be deleted.

The latency summary shares a common prefix and suffix; the following
shows the middle that differs.

- Instead of using `/wake_up`, the dual-pod controller goroutine
  working on the server-requesting Pod determines that it needs to
  create a new server-running Pod and that before doing that a
  sleeping instance has to be deleted. This goroutine sends to the
  kube-apiserver a request to delete the chosen server-running Pod.

- The kube-apiserver does its thing with the request to delete that
  Pod. This includes an etcd transaction. Note that this only _starts_
  the process of deletion (by updating a `.metadata` field of the
  Pod). The kube-apiserver returns a response to the deletion request
  and eventually sends a Pod update event to the kubelet. The dual-pod
  controller worker goroutine receives the reply to the deletion
  request and considers its job done.

- The kubelet receives the Pod update event and tears down the actual
  running pod. This is a non-trivial process, which I will mostly skip
  for brevity. I will note one thing: this includes telling the
  container runtime to kill the inference server container, which
  includes getting the OS to terminate the inference server; this in
  turn may involve signal handling by the inference server (I am not
  sure what vLLM actually does here). Once the pod is fully torn down,
  the kubelet sends a request to the kube-apiserver to update the Pod,
  specifically to remove the kubelet's
  [finalizer](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)
  (the presence of that finalizer has prevented the kube-apiserver
  from completing the deletion of the Pod).

- The kube-apiserver does its thing with this Pod update request. This
  particular update is the request that gets the Pod object fully
  deleted. This includes doing an etcd transaction. In an OpenShift
  cluster there are extra steps. The kube-apiserver eventually sends a
  Pod deletion event to the dual-pod controller.

- When the dual-pod controller gets the server-running Pod deletion
  event and enqueues a reference to that Pod. Eventually a worker
  gorouine dequeues that reference and works on that Pod. The worker
  sends to the kube-apiserver a request to create the new
  server-running Pod.

- The kube-apiserver does its thing with this Pod creation
  request. This includes doing an etcd transaction. In an OpenShift
  cluster there are extra steps. The kube-apiserver eventually sends a
  response back to the dual-pod controller and eventually sends Pod
  creation event to the relevant Node's kubelet.

- The dual-pod controller worker goroutine receives the Pod creation
  response. This does not yet show the IP address of the Pod (the
  kubelet will assign that). The worker goroutine notes the
  association between serve-requesting Pod and server-running Pod and
  is done.

- The kubelet receives the Pod creation event and begins creating the
  actual server-running pod. Again, let us suppose no delay for image
  pulling or volume creation/binding. The kubelet gets the inference
  server container created and the server's main process starts
  running. The kubelet sends a request to the kube-apiserver to update
  the Pod's status (including setting the Pod's IP address).

- The kube-apiserver does its thing with this Pod update request. This
  includes doing an etcd transaction. In an OpenShift cluster there
  are extra steps. The kube-apiserver eventually sends a Pod update
  event to the dual-pod controller.

- The dual-pod controller receives the server-running Pod update event
  and enqueues a reference to the server-requesting Pod. Eventually a
  worker goroutine dequeues that reference and works on it. This
  goroutine knows that the vLLM instance is not sleeping (how?
  probably because this same controller process previously requested
  the instance to be created and did not request it to sleep). This
  worker sends the POST request to tell the stub/requester what URL to
  poll for readiness.

- The stub/requester receives the POST request conveying the URL to
  poll for readiness. The stub/requester the sends the response, and
  then tries to send the readiness request to that URL.

- The vLLM instance may or may not even have the relevant port open
  yet. If the port is open, the instance may or may not be ready
  yet. In the unhappy cases, the stub/requester gets a failure or
  negative response, and waits a while and then tries again.

- Eventually the vLLM instance receives that readiness reques and
  sends a positive response.
