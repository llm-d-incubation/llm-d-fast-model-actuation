1- HOW TO RUN THE UNIT TEST:

Install all the necessary packages:
```bash
pip install -r requirements.txt
```

Run the unit test doing:
```bash
python -m pytest test_launcher.py -v
```

2- RUN E2E TEST:

Start the service:
```bash
uvicorn --port 8001 --log-level info launcher:app
```

Send commands (using HTTPie or cURL) with the following payload:
```json
{
  "options": "--model TinyLlama/TinyLlama-1.1B-Chat-v1.0 --port 8005",
  "env_vars": {
    "VLLM_USE_V1": "1",
    "VLLM_LOGGING_LEVEL": "DEBUG"
  }
}
```
For example, if using cURL, the command will be something like
```shell
curl -X POST http://localhost:8001/v2/vllm/instances \
  -H "Content-Type: application/json" \
  -d '{
    "options": "--model TinyLlama/TinyLlama-1.1B-Chat-v1.0 --port 8005",
    "env_vars": {
      "VLLM_USE_V1": "1",
      "VLLM_LOGGING_LEVEL": "DEBUG"
    }
  }'
```

The vLLM will start serving and you can request generations with the following payload:
```json
{
  "model": "TinyLlama/TinyLlama-1.1B-Chat-v1.0",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Tell me a joke about AI."}
  ],
  "temperature": 0.7,
  "max_tokens": 100
}
```
For example, if using cURL, the command will be something like
```shell
curl -X POST http://localhost:8005/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "TinyLlama/TinyLlama-1.1B-Chat-v1.0",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Tell me a joke about AI."}
    ],
    "temperature": 0.7,
    "max_tokens": 100
  }'
```
