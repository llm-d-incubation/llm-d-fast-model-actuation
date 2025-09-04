"""
vLLM Launcher
"""
from flask import Flask, jsonify, make_response
from .common import log_handlers # HTTP Status Codes
from http import HTTPStatus

# Create Flask application
app = Flask(__name__)


############################################################
# Health Endpoint
############################################################
@app.route("/health")
def health():
    """Health Status"""
    return jsonify(dict(status="OK")), HTTPStatus.OK


######################################################################
# GET INDEX
######################################################################
@app.route("/")
def index():
    """Root URL response"""
    return (
        jsonify(
            name="REST API Service",
            version="1.0",
        ),
        HTTPStatus.OK,
    )


######################################################################
# CREATE A NEW ACCOUNT
######################################################################
@app.route("/create_vllm", methods=["POST"])
def create_vllm():
    """
    Creates a vllm instance
    This endpoint will create a vllm instance
    """
    app.logger.info("Request to create an vLLM instance")
    message = {"vLLM instance created":"this is a test"}
    return make_response(
        jsonify(message), HTTPStatus.CREATED
    )


if __name__ == '__main__':
    app.run(port=8000, debug=True)
else:
    log_handlers.init_logging(app, "gunicorn.error")
