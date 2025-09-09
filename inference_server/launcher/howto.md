1- BASIC INSTALLATION FOR DEVELOPMENT/TESTING

- Activate the .venv
- install CPU-only pytorch
- use the vllm branch (already in my_stuff)
- set enviroment variable to disable the GPU
- run pip install to install vllm locally

```bash
source .venv/bin/activate
pip install torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cpu
cd ../vllm

export VLLM_TARGET_DEVICE=empty
pip install -e .
```

2- SOME TEST TO KEEP IN MIND

```python
    ## TEST 1 ##
    logger.info("Swap in an vLLM instance")
    message = {"vLLM instance created": "this is a test"}
    return JSONResponse(content=message, status_code=HTTPStatus.CREATED)

    ## TEST 2 ##
    # Check for MPS availability
    use_mps = torch.backends.mps.is_available()
    device_type = "mps" if use_mps else "cpu"
    print(f"Using device: {device_type}")
    # Initialize the LLM
    # Note: For macOS, you'll want to use smaller models
    llm = LLM(model="TinyLlama/TinyLlama-1.1B-Chat-v1.0",
            download_dir="./models",
            tensor_parallel_size=1,
            trust_remote_code=True,
            dtype="float16" if use_mps else "float32")
    # Set sampling parameters
    sampling_params = SamplingParams(temperature=0.7, top_p=0.95, max_tokens=100)
    # Generate text
    prompt = "Write a short poem about artificial intelligence."
    outputs = llm.generate([prompt], sampling_params)

    message = {"vLLM output": str(outputs)}
    return JSONResponse(content=message, status_code=HTTPStatus.CREATED)
```
