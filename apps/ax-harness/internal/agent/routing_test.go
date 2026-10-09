package agent

import (
	"reflect"
	"testing"
)

func TestUniqueRouteStages(t *testing.T) {
	tests := []struct {
		name      string
		stages    StageModels
		fallbacks map[string]string
		want      map[string]string
	}{
		{
			name: "distinct routes and approved alternates",
			stages: StageModels{Context: "context", Executor: "base", Responder: "responder",
				Skill: "skill", ExecutorEscalation: "strong"},
			fallbacks: map[string]string{"context": "context-spare", "base": "base-spare"},
			want: map[string]string{"context": "distiller", "context-spare": "distiller",
				"base": "executor", "base-spare": "executor", "strong": "executor",
				"responder": "responder", "skill": "skill_selection"},
		},
		{
			name:      "shared route is unattributed",
			stages:    StageModels{Context: "shared", Executor: "executor", Responder: "shared"},
			fallbacks: map[string]string{"shared": "spare"},
			want:      map[string]string{"executor": "executor"},
		},
		{
			name:      "fallback target assigned to another stage is unattributed",
			stages:    StageModels{Context: "context", Executor: "executor", Responder: "responder"},
			fallbacks: map[string]string{"context": "responder"},
			want:      map[string]string{"context": "distiller", "executor": "executor"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := uniqueRouteStages(tc.stages, tc.fallbacks); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("route stages = %#v, want %#v", got, tc.want)
			}
		})
	}
}
