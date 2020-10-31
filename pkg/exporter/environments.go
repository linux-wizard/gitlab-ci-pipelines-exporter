package exporter

import (
	"context"
	"time"

	"github.com/mvisonneau/gitlab-ci-pipelines-exporter/pkg/schemas"
	log "github.com/sirupsen/logrus"
)

func pullEnvironmentsFromProject(p schemas.Project) error {
	cfgUpdateLock.RLock()
	defer cfgUpdateLock.RUnlock()

	envIDs, err := gitlabClient.GetProjectEnvironmentIDs(p.Name, p.Pull.Environments.NameRegexp())
	if err != nil {
		return err
	}

	for _, envID := range envIDs {
		env := schemas.Environment{
			ProjectName: p.Name,
			ID:          envID,
			TagsRegexp:  p.Pull.Environments.TagsRegexp(),
		}

		envExists, err := store.EnvironmentExists(env.Key())
		if err != nil {
			return err
		}

		if !envExists {
			env, err = gitlabClient.GetEnvironment(env.ProjectName, env.ID)
			if err != nil {
				return err
			}

			log.WithFields(log.Fields{
				"project-name":     env.ProjectName,
				"environment-id":   env.ID,
				"environment-name": env.Name,
			}).Info("discovered new project environment")

			if err = store.SetEnvironment(env); err != nil {
				return err
			}

			go schedulePullEnvironmentMetrics(context.Background(), env)
		}
	}
	return nil
}

func pullEnvironmentMetrics(env schemas.Environment) (err error) {
	cfgUpdateLock.RLock()
	defer cfgUpdateLock.RUnlock()

	env, err = gitlabClient.GetEnvironment(env.ProjectName, env.ID)
	if err != nil {
		return
	}

	if err = store.SetEnvironment(env); err != nil {
		return
	}

	infoLabels := env.InformationLabelsValues()
	var commitDate time.Time
	if env.LatestDeployment.RefKind == schemas.RefKindBranch {
		infoLabels["latest_commit_short_id"], commitDate, err = gitlabClient.GetBranchLatestCommit(env.ProjectName, env.LatestDeployment.RefName)
	} else if env.LatestDeployment.RefKind == schemas.RefKindTag {
		infoLabels["latest_commit_short_id"], commitDate, err = gitlabClient.GetProjectMostRecentTagCommit(env.ProjectName, env.TagsRegexp)
	}

	if err != nil {
		return err
	}

	var (
		envBehindDurationSeconds float64
		envBehindCommitCount     float64
	)

	if infoLabels["latest_commit_short_id"] != infoLabels["current_commit_short_id"] {
		// To reduce the amount of compare requests being made, we check if the labels are unchanged since
		// the latest emission of the information metric
		exists, err := store.MetricExists(schemas.Metric{
			Kind:   schemas.MetricKindEnvironmentInformation,
			Labels: env.DefaultLabelsValues(),
		}.Key())

		if err != nil {
			return err
		}

		if !exists {
			commitCount, err := gitlabClient.GetCommitCountBetweenRefs(env.ProjectName, infoLabels["current_commit_short_id"], infoLabels["latest_commit_short_id"])
			if err != nil {
				return err
			}

			envBehindCommitCount = float64(commitCount)
		}
	}

	if commitDate.Sub(env.LatestDeployment.CreatedAt).Seconds() > 0 {
		envBehindDurationSeconds = commitDate.Sub(env.LatestDeployment.CreatedAt).Seconds()
	}

	storeSetMetric(schemas.Metric{
		Kind:   schemas.MetricKindEnvironmentBehindCommitsCount,
		Labels: env.DefaultLabelsValues(),
		Value:  envBehindCommitCount,
	})

	storeSetMetric(schemas.Metric{
		Kind:   schemas.MetricKindEnvironmentBehindDurationSeconds,
		Labels: env.DefaultLabelsValues(),
		Value:  envBehindDurationSeconds,
	})

	storeSetMetric(schemas.Metric{
		Kind:   schemas.MetricKindEnvironmentDeploymentDurationSeconds,
		Labels: env.DefaultLabelsValues(),
		Value:  env.LatestDeployment.Duration.Seconds(),
	})

	emitStatusMetric(
		schemas.MetricKindEnvironmentDeploymentStatus,
		env.DefaultLabelsValues(),
		statusesList[:],
		env.LatestDeployment.Status,
		// TODO: Respect project's config
		true,
	)

	storeSetMetric(schemas.Metric{
		Kind:   schemas.MetricKindEnvironmentDeploymentTimestamp,
		Labels: env.DefaultLabelsValues(),
		Value:  float64(env.LatestDeployment.CreatedAt.Unix()),
	})

	storeSetMetric(schemas.Metric{
		Kind:   schemas.MetricKindEnvironmentInformation,
		Labels: infoLabels,
		Value:  1,
	})

	return nil
}