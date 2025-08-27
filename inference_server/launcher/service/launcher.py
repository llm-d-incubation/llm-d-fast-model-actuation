"""
Account Service

This microservice handles the lifecycle of Accounts
"""
from flask import Flask, jsonify, make_response
from .common import status, log_handlers # HTTP Status Codes

# Create Flask application
app = Flask(__name__)


############################################################
# Health Endpoint
############################################################
@app.route("/health")
def health():
    """Health Status"""
    return jsonify(dict(status="OK")), status.HTTP_200_OK


######################################################################
# GET INDEX
######################################################################
@app.route("/")
def index():
    """Root URL response"""
    return (
        jsonify(
            name="Account REST API Service",
            version="1.0",
            # paths=url_for("list_accounts", _external=True),
        ),
        status.HTTP_200_OK,
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
        jsonify(message), status.HTTP_201_CREATED
    )


if __name__ == '__main__':
    app.run(port=8000, debug=True)
else:
    log_handlers.init_logging(app, "gunicorn.error")
