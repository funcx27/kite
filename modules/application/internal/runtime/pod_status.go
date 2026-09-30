package runtime

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/funcx27/kite/modules/application/internal/store"
)

// podStatus derives a kubectl-style display status for a pod. The raw phase
// stays "Pending" while an image pull or container start backs off, so the
// actionable reason (ImagePullBackOff, CrashLoopBackOff, ...) is read from
// the container waiting/terminated states instead (design §10).
func podStatus(pod *unstructured.Unstructured) string {
	if pod.GetDeletionTimestamp() != nil {
		return "Terminating"
	}
	phase := nestedString(pod.Object, "status", "phase")
	switch phase {
	case "Succeeded":
		return "Completed"
	case "Failed":
		if reason := containerReason(pod); reason != "" {
			return reason
		}
		return "Failed"
	}
	if reason := initStatus(pod); reason != "" {
		return reason
	}
	if reason := containerReason(pod); reason != "" {
		return reason
	}
	return phase
}

// initStatus describes init-container progress while the pod is still
// initializing: "Init:1/2", or the init failure ("Init:CrashLoopBackOff").
func initStatus(pod *unstructured.Unstructured) string {
	inits, _, _ := unstructured.NestedSlice(pod.Object, "status", "initContainerStatuses")
	total := len(inits)
	for i := 0; i < total; i++ {
		m, ok := store.AsMap(inits[i])
		if !ok {
			return fmt.Sprintf("Init:%d/%d", i, total)
		}
		state, _ := store.AsMap(m["state"])
		if t, ok := store.AsMap(state["terminated"]); ok && nestedString(t, "reason") == "Completed" {
			continue
		}
		if w, ok := store.AsMap(state["waiting"]); ok {
			if r := nestedString(w, "reason"); r != "" && r != "PodInitializing" {
				return "Init:" + r
			}
		}
		if t, ok := store.AsMap(state["terminated"]); ok {
			if r := nestedString(t, "reason"); r != "" {
				return "Init:" + r
			}
			return fmt.Sprintf("Init:ExitCode:%d", toInt64(t["exitCode"]))
		}
		return fmt.Sprintf("Init:%d/%d", i, total)
	}
	return ""
}

// containerReason returns the wait/termination reason of the newest
// non-ready container (iterating in reverse so the last failure wins):
// ImagePullBackOff, CrashLoopBackOff, ContainerCreating, Evicted, ... —
// empty when every container is running-ready.
func containerReason(pod *unstructured.Unstructured) string {
	statuses, _, _ := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
	for i := len(statuses) - 1; i >= 0; i-- {
		m, ok := store.AsMap(statuses[i])
		if !ok {
			continue
		}
		state, _ := store.AsMap(m["state"])
		if w, ok := store.AsMap(state["waiting"]); ok {
			if r := nestedString(w, "reason"); r != "" {
				return r
			}
		}
		if t, ok := store.AsMap(state["terminated"]); ok {
			if r := nestedString(t, "reason"); r != "" {
				return r
			}
			return fmt.Sprintf("ExitCode:%d", toInt64(t["exitCode"]))
		}
	}
	return ""
}
