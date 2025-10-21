# Test requester

This is a variant of the normal requester that does not not actually involve GPUs.
Instead of running `nvidia-smi` to discover what GPUs were assigned by the kubelet,
this requester maintains GPU assignments in a ConfigMap named "gpu-allocs".

For details, see the comment at the start of [the source](main.go).

The command line arguments are as follows.
The Pod ID can be anything (e.g., name, UID); use something that gives good replacement semantics.

## Requester specific arguments

```console
      --node string                      name of this Pod's Node
      --num-gpus int                     number of GPUs to allocate (default 1)
      --pod-id string                    ID of this Pod
      --probes-port int16                port number for /ready (default 8080)
      --spi-port int16                   port for dual-pods requests (default 8081)
```

## kubectl arguments

```console
      --cluster string                   The name of the kubeconfig cluster to use
      --context string                   The name of the kubeconfig context to use
      --kubeconfig string                Path to the kubeconfig file to use
  -n, --namespace string                 The name of the Kubernetes Namespace to work in (NOT optional)
      --user string                      The name of the kubeconfig user to use
```

## Logging arguments

```console
      --add_dir_header                   If true, adds the file directory to the header of the log messages
      --alsologtostderr                  log to standard error as well as files (no effect when -logtostderr=true)
      --log_backtrace_at traceLocation   when logging hits line file:N, emit a stack trace (default :0)
      --log_dir string                   If non-empty, write log files in this directory (no effect when -logtostderr=true)
      --log_file string                  If non-empty, use this log file (no effect when -logtostderr=true)
      --log_file_max_size uint           Defines the maximum size a log file can grow to (no effect when -logtostderr=true). Unit is megabytes. If the value is 0, the maximum file size is unlimited. (default 1800)
      --logtostderr                      log to standard error instead of files (default true)
      --one_output                       If true, only write logs to their native severity level (vs also writing to each lower severity level; no effect when -logtostderr=true)
      --skip_headers                     If true, avoid header prefixes in the log messages
      --skip_log_headers                 If true, avoid headers when opening log files (no effect when -logtostderr=true)
      --stderrthreshold severity         logs at or above this threshold go to stderr when writing to files and stderr (no effect when -logtostderr=true or -alsologtostderr=true) (default 2)
  -v, --v Level                          number for the log level verbosity
      --vmodule moduleSpec               comma-separated list of pattern=N settings for file-filtered logging
```
