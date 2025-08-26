The llm-d-fast-model-actuation repository contains work on two of the
many areas of work that contribute to fast model actuation. The two
areas here are as follows.

The [model-flexibility](model-flexibility) directory concerns the use
of vLLM sleep level 1 sleep and wake, and of vLLM model swapping. A
level 1 sleep stops the inferencing and evicts the model tensors from
accelerator memory, keeping it in main memory instead. This frees up
all of the accelerator's compute capacity and most of its memory
capacity for use by other processes. A wake-up operation can quickly
copy that model tensors back to the accelartor. These are available
today and a subdirectory contains a technology demonstration of using
this in the context of vllm's Production Stack. The main content of
the `model-flexibility` directory is an incubating project to use this
technology in llm-d (taking a different approach than the technology
demonstration).

The [model-caching](model-caching) directory concerns the caching and
staging of models and torch compilations. The intent is to build on
what is currently in KServe. There is a subdirectory that holds a
technology demonstration of using some other ideas in this area.
