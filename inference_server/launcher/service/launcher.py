"""
vLLM Launcher - FastAPI Version
"""

import logging
from http import HTTPStatus  # HTTP Status Codes

from fastapi import FastAPI
from fastapi.responses import JSONResponse

# Create FastAPI application
app = FastAPI(
    title="REST API Service", version="1.0", description="vLLM Management API"
)

# Setup logging
logger = logging.getLogger(__name__)


############################################################
# Health Endpoint
############################################################
@app.get("/health")
async def health():
    """Health Status"""
    return JSONResponse(content={"status": "OK"}, status_code=HTTPStatus.OK)


######################################################################
# GET INDEX
######################################################################
@app.get("/")
async def index():
    """Root URL response"""
    return JSONResponse(
        content={"name": "REST API Service", "version": "1.0"},
        status_code=HTTPStatus.OK,
    )


######################################################################
# vLLM MANAGEMENT ENDPOINTS
######################################################################
@app.post("/vllm")
async def create_vllm():
    """Create/swap in a new vLLM instance"""
    logger.info("Swap in an vLLM instance")
    message = {"vLLM instance created": "this is a test"}
    return JSONResponse(content=message, status_code=HTTPStatus.CREATED)


@app.delete("/vllm")
async def delete_vllm():
    """Delete/swap out the vLLM instance"""
    logger.info("Swap out vLLM instance")
    message = {"vLLM instance deleted": "this is a test"}
    return JSONResponse(content=message, status_code=HTTPStatus.ACCEPTED)


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=8000, log_level="info")
