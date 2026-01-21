---
{{- if .Values.policies.enabled }}
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: fma-immutable-fields
  annotations:
    description: "Deny mutation of controller-managed annotations and labels unless performed by an FMA controller service account."
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        resources: ["pods"]
        operations: ["UPDATE"]
  validations:
    # The expression below denies update operations that attempt to change
    # annotations/labels that are managed by FMA controllers. It permits
    # such changes only when the requester is an FMA controller service
    # account (launcher-populator or dual-pods-controller).
    - expression: |
        request.userInfo.username.matches("^system:serviceaccount:[^:]+:(launcher-populator|dual-pods-controller)$") ||
        (
          oldObject.metadata.?annotations['dual-pods.llm-d.ai/requester'].orValue('') == object.metadata.?annotations['dual-pods.llm-d.ai/requester'].orValue('') &&
          oldObject.metadata.?annotations['dual-pods.llm-d.ai/status'].orValue('') == object.metadata.?annotations['dual-pods.llm-d.ai/status'].orValue('') &&
          oldObject.metadata.?labels['dual-pods.llm-d.ai/dual'].orValue('') == object.metadata.?labels['dual-pods.llm-d.ai/dual'].orValue('')
        )
      message: "One or more annotations/labels are managed by FMA controllers and cannot be modified directly."

    {{- end }}
