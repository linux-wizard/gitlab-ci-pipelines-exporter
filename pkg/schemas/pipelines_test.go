package schemas

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	goGitlab "github.com/xanzy/go-gitlab"
)

func TestNewPipeline(t *testing.T) {
	updatedAt := time.Date(2020, 10, 1, 13, 5, 10, 0, time.UTC)

	gitlabPipeline := goGitlab.Pipeline{
		ID:        20,
		Coverage:  "25.6",
		UpdatedAt: &updatedAt,
		Duration:  15,
		Status:    "pending",
	}

	expectedPipeline := Pipeline{
		ID:              20,
		Coverage:        25.6,
		Timestamp:       1.60155751e+09,
		DurationSeconds: 15,
		Status:          "pending",
	}

	assert.Equal(t, expectedPipeline, NewPipeline(gitlabPipeline))
}

func TestRunningPipeline(t *testing.T) {
	createdAt := time.Date(2020, 10, 1, 13, 4, 10, 0, time.UTC)
	startedAt := time.Date(2020, 10, 1, 13, 5, 10, 0, time.UTC)
	updatedAt := time.Date(2020, 10, 1, 13, 5, 10, 0, time.UTC)

	gitlabPipeline := goGitlab.Pipeline{
		ID:        21,
		Coverage:  "25.6",
		CreatedAt: &createdAt,
		StartedAt: &startedAt,
		UpdatedAt: &updatedAt,
		Duration:  15,
		Status:    "running",
	}

	expectedPipeline := Pipeline{
		ID:                    21,
		Coverage:              25.6,
		Timestamp:             1.60155751e+09,
		DurationSeconds:       15,
		QueuedDurationSeconds: 60,
		Status:                "running",
	}

	assert.Equal(t, expectedPipeline, NewPipeline(gitlabPipeline))
}
