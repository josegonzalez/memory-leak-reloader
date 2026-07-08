package controller

import (
	"sort"

	corev1 "k8s.io/api/core/v1"

	"github.com/josegonzalez/memory-leak-reloader/internal/config"
)

// Target is a container selected for monitoring, with its effective detection
// config and current memory limit (bytes; 0 if unset).
type Target struct {
	Name       string
	LimitBytes int64
	Det        config.Detection
}

// candidate is an eligible container (regular container or native sidecar).
type candidate struct {
	name  string
	limit int64
}

// candidates returns the monitorable container set: all regular containers plus
// native sidecars (init containers with restartPolicy: Always). Plain init
// containers are excluded.
func candidates(pod *corev1.Pod) []candidate {
	out := make([]candidate, 0, len(pod.Spec.Containers))
	for i := range pod.Spec.Containers {
		c := &pod.Spec.Containers[i]
		out = append(out, candidate{name: c.Name, limit: memLimit(c)})
	}
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			out = append(out, candidate{name: c.Name, limit: memLimit(c)})
		}
	}
	return out
}

func memLimit(c *corev1.Container) int64 {
	if c.Resources.Limits == nil {
		return 0
	}
	q := c.Resources.Limits.Memory()
	if q == nil {
		return 0
	}
	return q.Value()
}

// SelectTargets resolves which containers to monitor for a pod and their
// effective detection config. The second return is the number of selected
// containers dropped because they have neither a memory limit nor an absolute
// hard-cap (so they are unmonitorable). When the returned target slice is empty
// but dropped > 0, the pod is opted-in-but-unmonitorable.
func SelectTargets(pod *corev1.Pod, podCfg config.PodConfig) (targets []Target, dropped int) {
	cands := candidates(pod)
	byName := make(map[string]candidate, len(cands))
	for _, c := range cands {
		byName[c.name] = c
	}

	var selected []candidate
	switch {
	case len(podCfg.Containers) == 0:
		if c, ok := defaultContainer(pod, cands); ok {
			selected = []candidate{c}
		}
	case len(podCfg.Containers) == 1 && podCfg.Containers[0] == config.ContainersAll:
		selected = cands
	default:
		for _, name := range podCfg.Containers {
			if c, ok := byName[name]; ok {
				selected = append(selected, c)
			}
		}
	}

	for _, c := range selected {
		det := podCfg.ForContainer(c.name)
		if c.limit <= 0 && det.ThresholdAbsolute == nil {
			dropped++
			continue
		}
		targets = append(targets, Target{Name: c.name, LimitBytes: c.limit, Det: det})
	}
	return targets, dropped
}

// defaultContainer picks the single default container: the one named by the
// default-container annotation if eligible, else the highest-memory-limit
// container (deterministic name-sort tiebreak). Returns false if no candidate
// has a memory limit and there is no default-container annotation.
func defaultContainer(pod *corev1.Pod, cands []candidate) (candidate, bool) {
	if name := pod.Annotations[config.AnnotationDefaultContainer]; name != "" {
		for _, c := range cands {
			if c.name == name {
				return c, true
			}
		}
	}
	// Highest limit; tiebreak by name. Only containers with a limit qualify.
	withLimit := make([]candidate, 0, len(cands))
	for _, c := range cands {
		if c.limit > 0 {
			withLimit = append(withLimit, c)
		}
	}
	if len(withLimit) == 0 {
		return candidate{}, false
	}
	sort.Slice(withLimit, func(i, j int) bool {
		if withLimit[i].limit != withLimit[j].limit {
			return withLimit[i].limit > withLimit[j].limit
		}
		return withLimit[i].name < withLimit[j].name
	})
	return withLimit[0], true
}
