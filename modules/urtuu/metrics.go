/*
 * Gerege Nexus
 * Copyright (c) 2026 Gerege Systems Development Team, Gerege Nomadica Foundation
 * Distributed under the Apache 2.0 License.
 *
 * What the board exports to Prometheus: one counter, no tenant label.
 */

package urtuu

import "github.com/prometheus/client_golang/prometheus"

// tasksTotal counts a task arriving at a status — every transition and every
// task raised, by where it landed.
//
// A counter and not a gauge of how many tasks are in each state. A gauge would
// have to be recomputed from the table on a timer and would say nothing about
// what happened between two scrapes; a counter answers the questions that
// actually get asked of it — how much work is coming in, how much is being
// returned, how much is finishing — and the current standing is a database
// question the board already answers.
//
// No tenant label, per the rule in internal/platform/observability/business.go.
var tasksTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "urtuu_tasks_total",
		Help: "Өртөө tasks reaching each status, counting every transition",
	},
	[]string{"status"},
)

func init() { prometheus.MustRegister(tasksTotal) }
