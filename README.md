The llm-d-fast-model-actuation repository contains work on two of the
many areas of work that contribute to fast model actuation. The two
areas here are as follows.

The [model-flexibility](model-flexibility) workstream concerns the use
of pools to accelerate vLLM instance time to readiness. It explores
various pooling mecanisms, ranging from:
- having a thin vLLM launcher for swapping models on the fly
- to exploiting vLLM sleep level 1 capability. 

A Level 1 sleep stops the inferencing and evicts the model tensors from
accelerator memory, keeping it in main memory instead. This frees up
all of the accelerator's compute capacity and most of its memory
capacity for use by other processes. A wake-up operation can quickly
copy that model tensors back to the accelerator. 

The [model-caching](model-caching) workstream concerns the caching and
staging of models and torch compilations. The intent is to build on
what is currently in KServe and extends it to support other artifacts
(eg. pytorch compilation artifacts).

