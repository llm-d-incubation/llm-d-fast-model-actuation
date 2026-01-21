---
{{- if .Values.policies.enabled }}
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: fma-bound-serverreqpod
  annotations:
    description: "Deny changes to annotations for bound Server Requesting Pods unless performed by an FMA controller service account."
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        resources: ["pods"]
        operations: ["UPDATE"]
  validations:
    # Deny changes from users to controller-maintained annotations/labels
    # once a server-requesting Pod is bound to a server-providing Pod.
    #
    # Logic:
    # 1) If the old object is a server requesting pod (`inference-server-config`),
    # 2) AND the `dual` label is present and non-empty (bound state),
    # 3) AND any of the immutable fields change
    # 4) THEN deny unless the request originates from an FMA controller SA.
    - expression: |
        request.userInfo.username.matches("^system:serviceaccount:[^:]+:(launcher-populator|dual-pods-controller)$") ||
        !(
          oldObject.metadata.?annotations['dual-pods.llm-d.ai/inference-server-config'].orValue('') != "" &&
          oldObject.metadata.?labels['dual-pods.llm-d.ai/dual'].orValue('') != "" &&
          (
            oldObject.metadata.?annotations['dual-pods.llm-d.ai/inference-server-config'].orValue('') != object.metadata.?annotations['dual-pods.llm-d.ai/inference-server-config'].orValue('') ||
            oldObject.metadata.?labels['dual-pods.llm-d.ai/dual'].orValue('') != object.metadata.?labels['dual-pods.llm-d.ai/dual'].orValue('')
          )
        )
      message: "One or more annotations/labels are managed by FMA controllers and cannot be changed once the server requesting pod is bound to a providing pod."

    {{- end }}
