{{- define "to_millicores" -}}
  {{- $value := toString . -}}
  {{- if hasSuffix "m" $value -}}
    {{ trimSuffix "m" $value }}
  {{- else -}}
    {{ mulf $value 1000 }}
  {{- end -}}
{{- end -}}
 
{{/* Define new HPA parameters based on old if old parameters presented */}}
{{- define "hpa_bwc" -}}
    {{- $valuesMap := .Values -}}
    {{ $paramValue := int $valuesMap.HPA_SCALING_UP_INTERVAL -}}
    {{- if and (hasKey .Values "HPA_SCALING_UP_INTERVAL") (ge $paramValue 0) -}}
        {{- $noop := set $valuesMap "HPA_SCALING_UP_STABILIZATION_WINDOW_SECONDS" $paramValue -}}
        {{- $noop := set $valuesMap "HPA_SCALING_UP_SELECT_POLICY" "Max" -}}
        {{- $noop := set $valuesMap "HPA_SCALING_UP_PERCENT_VALUE" 95 -}}
        {{- $noop := set $valuesMap "HPA_SCALING_UP_PERCENT_PERIOD_SECONDS" $paramValue -}}
        {{- $noop := unset $valuesMap "HPA_SCALING_UP_PODS_VALUE" -}}
        {{- $noop := unset $valuesMap "HPA_SCALING_UP_PODS_PERIOD_SECONDS" -}}
    {{- end -}}
    {{ $paramValue := int $valuesMap.HPA_SCALING_DOWN_INTERVAL -}}
    {{- if and (hasKey .Values "HPA_SCALING_DOWN_INTERVAL") (ge $paramValue 0) -}}
        {{- $paramValue := get $valuesMap "HPA_SCALING_DOWN_INTERVAL" -}}
        {{- $noop := set $valuesMap "HPA_SCALING_DOWN_STABILIZATION_WINDOW_SECONDS" $paramValue -}}
        {{- $noop := set $valuesMap "HPA_SCALING_DOWN_SELECT_POLICY" "Max" -}}
        {{- $noop := set $valuesMap "HPA_SCALING_DOWN_PERCENT_VALUE" 95 -}}
        {{- $noop := set $valuesMap "HPA_SCALING_DOWN_PERCENT_PERIOD_SECONDS" $paramValue -}}
        {{- $noop := unset $valuesMap "HPA_SCALING_DOWN_PODS_VALUE" -}}
        {{- $noop := unset $valuesMap "HPA_SCALING_DOWN_PODS_PERIOD_SECONDS" -}}
    {{- end -}}
{{- end -}}

{{/*
Find an image in various places. Image can be found from:
* specified by user from .Values.imageRepository and .Values.imageTag
* default value
*/}}
{{- define "otec.image" -}}
  {{- if and (not (empty .Values.imageRepository)) (not (empty .Values.imageTag)) -}}
    {{- printf "%s:%s" .Values.imageRepository .Values.imageTag -}}
  {{- else -}}
    {{- printf "qubership-open-telemetry-collector:latest" -}}
  {{- end -}}
{{- end -}}