package controller

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mvisonneau/gitlab-ci-pipelines-exporter/pkg/config"
	"github.com/mvisonneau/gitlab-ci-pipelines-exporter/pkg/schemas"
	log "github.com/sirupsen/logrus"
	goGitlab "github.com/xanzy/go-gitlab"
)

func (c *Controller) processPipelineEvent(e goGitlab.PipelineEvent) {
	var refKind schemas.RefKind
	refName := e.ObjectAttributes.Ref

	// TODO: Perhaps it would be nice to match upon the regexp to validate
	// that it is actually a merge request ref
	if e.MergeRequest.IID != 0 {
		refKind = schemas.RefKindMergeRequest
		refName = strconv.Itoa(e.MergeRequest.IID)
	} else if e.ObjectAttributes.Tag {
		refKind = schemas.RefKindTag
	} else {
		refKind = schemas.RefKindBranch
	}

	c.triggerRefMetricsPull(schemas.NewRef(
		schemas.NewProject(e.Project.PathWithNamespace),
		refKind,
		refName,
	))
}

func (c *Controller) triggerRefMetricsPull(ref schemas.Ref) {
	logFields := log.Fields{
		"project-name": ref.Project.Name,
		"ref":          ref.Name,
		"ref-kind":     ref.Kind,
	}

	refExists, err := c.Store.RefExists(ref.Key())
	if err != nil {
		log.WithFields(logFields).WithError(err).Error("reading ref from the store")
		return
	}

	// Let's try to see if the project is configured to export this ref
	if !refExists {
		p := schemas.NewProject(ref.Project.Name)

		projectExists, err := c.Store.ProjectExists(p.Key())
		if err != nil {
			log.WithFields(logFields).WithError(err).Error("reading project from the store")
			return
		}

		// Perhaps the project is discoverable through a wildcard
		if !projectExists && len(c.Config.Wildcards) > 0 {
			for id, w := range c.Config.Wildcards {
				// If in all our wildcards we have one which can potentially match the project ref
				// received, we trigger a scan
				matches, err := isRefMatchingWilcard(w, ref)
				if err != nil {
					log.WithError(err).Warn("checking if the ref matches the wildcard config")
					continue
				}

				if matches {
					c.ScheduleTask(context.TODO(), schemas.TaskTypePullProjectsFromWildcard, strconv.Itoa(id), strconv.Itoa(id), w)
					log.WithFields(logFields).Info("project ref not currently exported but its configuration matches a wildcard, triggering a pull of the projects from this wildcard")
				} else {
					log.WithFields(logFields).Debug("project ref not matching wildcard, skipping..")
				}
			}
			log.WithFields(logFields).Info("done looking up for wildcards matching the project ref")
			return
		}

		if projectExists {
			// If the project exists, we check that the ref matches it's configuration
			if err := c.Store.GetProject(&p); err != nil {
				log.WithFields(logFields).WithError(err).Error("reading project from the store")
				return
			}

			matches, err := isRefMatchingProjectPullRefs(p.Pull.Refs, ref)
			if err != nil {
				log.WithError(err).Error("checking if the ref matches the project config")
				return
			}

			if matches {
				ref.Project = p
				if err = c.Store.SetRef(ref); err != nil {
					log.WithFields(logFields).WithError(err).Error("writing ref in the store")
					return
				}
				goto schedulePull
			}
		}

		log.WithFields(logFields).Info("ref not configured in the exporter, ignoring pipeline webhook")
		return
	}

schedulePull:
	log.WithFields(logFields).Info("received a pipeline webhook from GitLab for a ref, triggering metrics pull")
	// TODO: When all the metrics will be sent over the webhook, we might be able to avoid redoing a pull
	// eg: 'coverage' is not in the pipeline payload yet, neither is 'artifacts' in the job one
	c.ScheduleTask(context.TODO(), schemas.TaskTypePullRefMetrics, string(ref.Key()), ref)
}

func (c *Controller) processDeploymentEvent(e goGitlab.DeploymentEvent) {
	c.triggerEnvironmentMetricsPull(schemas.Environment{
		ProjectName: e.Project.PathWithNamespace,
		Name:        e.Environment,
	})
}

func (c *Controller) triggerEnvironmentMetricsPull(env schemas.Environment) {
	logFields := log.Fields{
		"project-name":     env.ProjectName,
		"environment-name": env.Name,
	}

	envExists, err := c.Store.EnvironmentExists(env.Key())
	if err != nil {
		log.WithFields(logFields).WithError(err).Error("reading environment from the store")
		return
	}

	if !envExists {
		p := schemas.NewProject(env.ProjectName)

		projectExists, err := c.Store.ProjectExists(p.Key())
		if err != nil {
			log.WithFields(logFields).WithError(err).Error("reading project from the store")
			return
		}

		// Perhaps the project is discoverable through a wildcard
		if !projectExists && len(c.Config.Wildcards) > 0 {
			for id, w := range c.Config.Wildcards {
				// If in all our wildcards we have one which can potentially match the env
				// received, we trigger a scan
				matches, err := isEnvMatchingWilcard(w, env)
				if err != nil {
					log.WithError(err).Warn("checking if the env matches the wildcard config")
					continue
				}

				if matches {
					c.ScheduleTask(context.TODO(), schemas.TaskTypePullProjectsFromWildcard, strconv.Itoa(id), strconv.Itoa(id), w)
					log.WithFields(logFields).Info("project environment not currently exported but its configuration matches a wildcard, triggering a pull of the projects from this wildcard")
				} else {
					log.WithFields(logFields).Debug("project ref not matching wildcard, skipping..")
				}
			}
			log.WithFields(logFields).Info("done looking up for wildcards matching the project ref")
			return
		}

		if projectExists {
			if err := c.Store.GetProject(&p); err != nil {
				log.WithFields(logFields).WithError(err).Error("reading project from the store")
			}

			matches, err := isEnvMatchingProjectPullEnvironments(p.Pull.Environments, env)
			if err != nil {
				log.WithError(err).Error("checking if the env matches the project config")
				return
			}

			if matches {
				// As we do not get the environment ID within the deployment event, we need to query it back..
				if err = c.UpdateEnvironment(&env); err != nil {
					log.WithFields(logFields).WithError(err).Error("updating event from GitLab API")
					return
				}
				goto schedulePull
			}
		}

		log.WithFields(logFields).Info("environment not configured in the exporter, ignoring deployment webhook")
		return
	}

	// Need to refresh the env from the store in order to get at least it's ID
	if env.ID == 0 {
		if err = c.Store.GetEnvironment(&env); err != nil {
			log.WithFields(logFields).WithError(err).Error("reading environment from the store")
		}
	}

schedulePull:
	log.WithFields(logFields).Info("received a deployment webhook from GitLab for an environment, triggering metrics pull")
	c.ScheduleTask(context.TODO(), schemas.TaskTypePullEnvironmentMetrics, string(env.Key()), env)
}

func isRefMatchingProjectPullRefs(pprs config.ProjectPullRefs, ref schemas.Ref) (matches bool, err error) {
	// We check if the ref kind is enabled
	switch ref.Kind {
	case schemas.RefKindBranch:
		if !pprs.Branches.Enabled {
			return
		}
	case schemas.RefKindTag:
		if !pprs.Tags.Enabled {
			return
		}
	case schemas.RefKindMergeRequest:
		if !pprs.MergeRequests.Enabled {
			return
		}
	default:
		return false, fmt.Errorf("invalid ref kind %v", ref.Kind)
	}

	// Then we check if it matches the regexp
	var re *regexp.Regexp
	if re, err = schemas.GetRefRegexp(pprs, ref.Kind); err != nil {
		return
	}
	return re.MatchString(ref.Name), nil
}

func isEnvMatchingProjectPullEnvironments(ppe config.ProjectPullEnvironments, env schemas.Environment) (matches bool, err error) {
	// We check if the environments pulling is enabled
	if !ppe.Enabled {
		return
	}

	// Then we check if it matches the regexp
	var re *regexp.Regexp
	if re, err = regexp.Compile(ppe.Regexp); err != nil {
		return
	}
	return re.MatchString(env.Name), nil
}

func isRefMatchingWilcard(w config.Wildcard, ref schemas.Ref) (matches bool, err error) {
	// Then we check if the owner matches the ref or is global
	if w.Owner.Kind != "" && !strings.Contains(ref.Project.Name, w.Owner.Name) {
		return
	}

	// Then we check if the ref matches the project pull parameters
	return isRefMatchingProjectPullRefs(w.Pull.Refs, ref)
}

func isEnvMatchingWilcard(w config.Wildcard, env schemas.Environment) (matches bool, err error) {
	// Then we check if the owner matches the ref or is global
	if w.Owner.Kind != "" && !strings.Contains(env.ProjectName, w.Owner.Name) {
		return
	}

	// Then we check if the ref matches the project pull parameters
	return isEnvMatchingProjectPullEnvironments(w.Pull.Environments, env)
}