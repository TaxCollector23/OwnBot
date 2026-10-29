package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

type automationRecord struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	TriggerType     string `json:"trigger_type"`
	AgentID         string `json:"agent_id"`
	IntervalMinutes int    `json:"interval_minutes"`
	TimeUTC         string `json:"time_utc"`
	NextRunAt       string `json:"next_run_at"`
}

type automationAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type automationJob struct {
	ID           string `json:"id"`
	AutomationID string `json:"automation_id"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	RunAfter     string `json:"run_after"`
}

// runAutomationSetup is the CLI entrypoint for `ownbot setup automation ...`.
func runAutomationSetup(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ownbot setup automation list|add|run")
	}
	operation := args[0]
	args = args[1:]
	if operation != "list" && operation != "add" && operation != "run" {
		return fmt.Errorf("unknown automation operation %q; use list, add, or run", operation)
	}
	fs := flag.NewFlagSet("setup automation "+operation, flag.ContinueOnError)
	fs.SetOutput(stderr)
	urlFlag := fs.String("url", "", "loopback URL of the local daemon")
	dataDirDefault, _ := defaultDataDir()
	dataDir := fs.String("data-dir", dataDirDefault, "daemon state directory containing api.token")
	name := fs.String("name", "", "automation name")
	agent := fs.String("agent", "", "agent name or ID")
	input := fs.String("input", "", "required input text for each run")
	interval := fs.Int("interval-minutes", 0, "explicit interval schedule (1 to 1440 minutes)")
	dailyAt := fs.String("daily-at", "", "explicit daily schedule time in UTC (HH:MM)")
	var enabledFlag optionalBool
	fs.Var(&enabledFlag, "enabled", "enable automation (true or false); schedules are disabled by default")
	idempotencyKey := fs.String("idempotency-key", "", "caller-chosen key reused to deduplicate this run")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("automation operation does not accept positional arguments")
	}
	client, err := clientFor(*urlFlag, *dataDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch operation {
	case "list":
		return listAutomations(ctx, client, stdout)
	case "add":
		return addAutomation(ctx, client, *name, *agent, *input, *interval, *dailyAt, string(enabledFlag), stdout)
	case "run":
		if strings.TrimSpace(*idempotencyKey) == "" {
			return errors.New("--idempotency-key is required; reuse the same value when retrying this run")
		}
		if len(*idempotencyKey) > 200 {
			return errors.New("--idempotency-key must be at most 200 characters")
		}
		if strings.TrimSpace(*name) == "" {
			return errors.New("--name is required to identify the automation")
		}
		return runAutomationJob(ctx, client, *name, *idempotencyKey, stdout)
	}
	return nil
}

func listAutomations(ctx context.Context, client *Client, stdout io.Writer) error {
	var automations []automationRecord
	if err := client.Do(ctx, "GET", "/api/automations", nil, &automations); err != nil {
		return fmt.Errorf("could not list automations: %w", err)
	}
	var jobs []automationJob
	if err := client.Do(ctx, "GET", "/api/jobs", nil, &jobs); err != nil {
		return fmt.Errorf("could not list jobs: %w", err)
	}
	if len(automations) == 0 {
		_, _ = fmt.Fprintln(stdout, "No automations.")
	} else {
		_, _ = fmt.Fprintln(stdout, "Automations:")
		for i, item := range automations {
			if i == 50 {
				_, _ = fmt.Fprintf(stdout, "  … %d more automations\n", len(automations)-i)
				break
			}
			enabled := "disabled"
			if item.Enabled {
				enabled = "enabled"
			}
			schedule := item.TriggerType
			if item.TriggerType == "interval" {
				schedule = fmt.Sprintf("every %d minutes", item.IntervalMinutes)
			} else if item.TriggerType == "daily_utc" {
				schedule = "daily at " + item.TimeUTC + " UTC"
			}
			_, _ = fmt.Fprintf(stdout, "  %s  %s  (%s, %s, agent %s)", item.ID, item.Name, enabled, schedule, item.AgentID)
			if item.NextRunAt != "" {
				_, _ = fmt.Fprintf(stdout, "  next %s", item.NextRunAt)
			}
			_, _ = fmt.Fprintln(stdout)
		}
	}
	if len(jobs) == 0 {
		_, _ = fmt.Fprintln(stdout, "No jobs.")
	} else {
		_, _ = fmt.Fprintln(stdout, "Recent jobs:")
		for i, job := range jobs {
			if i == 20 {
				_, _ = fmt.Fprintf(stdout, "  … %d more jobs\n", len(jobs)-i)
				break
			}
			_, _ = fmt.Fprintf(stdout, "  %s  %s  automation %s  attempts %d  next %s\n", job.ID, job.Status, job.AutomationID, job.Attempts, job.RunAfter)
		}
	}
	return nil
}

func addAutomation(ctx context.Context, client *Client, name, agentName, input string, interval int, dailyAt, enabledValue string, stdout io.Writer) error {
	name, agentName, input = strings.TrimSpace(name), strings.TrimSpace(agentName), strings.TrimSpace(input)
	if name == "" || len(name) > 100 {
		return errors.New("--name is required and must be at most 100 characters")
	}
	if agentName == "" {
		return errors.New("--agent is required")
	}
	if input == "" || len(input) > 64000 {
		return errors.New("--input is required and must be at most 64000 characters")
	}
	if interval < 0 || interval > 1440 {
		return errors.New("--interval-minutes must be 1 to 1440")
	}
	if interval != 0 && dailyAt != "" {
		return errors.New("choose either --interval-minutes or --daily-at, not both")
	}
	trigger := "manual"
	if interval != 0 {
		trigger = "interval"
	} else if dailyAt != "" {
		trigger = "daily_utc"
		if !validAutomationUTCTime(dailyAt) {
			return errors.New("--daily-at must use a valid HH:MM UTC time")
		}
	}
	if enabledValue != "" && enabledValue != "true" && enabledValue != "false" {
		return errors.New("--enabled must be true or false")
	}
	enabled := trigger == "manual"
	if enabledValue != "" {
		enabled = enabledValue == "true"
	}
	if trigger == "manual" && !enabled {
		return errors.New("manual automations must be enabled to be run; they do not execute until an explicit run command")
	}

	var agents []automationAgent
	if err := client.Do(ctx, "GET", "/api/agents", nil, &agents); err != nil {
		return fmt.Errorf("could not find agent: %w", err)
	}
	resolvedAgent, err := uniqueAutomationMatch(agentName, agents, func(a automationAgent) string { return a.ID }, func(a automationAgent) string { return a.Name })
	if err != nil {
		return fmt.Errorf("agent %q: %w", agentName, err)
	}
	body := map[string]any{"name": name, "enabled": enabled, "trigger_type": trigger, "agent_id": resolvedAgent.ID, "input": input}
	if interval != 0 {
		body["interval_minutes"] = interval
	}
	if dailyAt != "" {
		body["time_utc"] = dailyAt
	}
	var created automationRecord
	if err := client.Do(ctx, "POST", "/api/automations", body, &created); err != nil {
		return fmt.Errorf("could not create automation: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "Created %q (%s, %s) with agent %s.\n", created.Name, created.ID, created.TriggerType, created.AgentID)
	if !created.Enabled && created.TriggerType != "manual" {
		_, _ = fmt.Fprintln(stdout, "Schedule is disabled; enable it explicitly with --enabled=true when creating a schedule.")
	}
	return nil
}

func runAutomationJob(ctx context.Context, client *Client, identifier, key string, stdout io.Writer) error {
	var automations []automationRecord
	if err := client.Do(ctx, "GET", "/api/automations", nil, &automations); err != nil {
		return fmt.Errorf("could not find automation: %w", err)
	}
	automation, err := uniqueAutomationMatch(identifier, automations, func(a automationRecord) string { return a.ID }, func(a automationRecord) string { return a.Name })
	if err != nil {
		return fmt.Errorf("automation %q: %w", identifier, err)
	}
	if !automation.Enabled {
		return errors.New("automation is disabled; enable it before queuing a run")
	}
	var job struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		Deduplicated bool   `json:"deduplicated"`
	}
	path := "/api/automations/" + automation.ID + "/jobs"
	if err := client.Do(ctx, "POST", path, map[string]string{"idempotency_key": key}, &job); err != nil {
		return fmt.Errorf("could not queue automation run: %w", err)
	}
	if job.Deduplicated {
		_, _ = fmt.Fprintf(stdout, "Run already queued as job %s (same idempotency key).\n", job.ID)
	} else {
		_, _ = fmt.Fprintf(stdout, "Queued job %s for automation %q (%s).\n", job.ID, automation.Name, job.Status)
	}
	return nil
}

func uniqueAutomationMatch[T any](identifier string, items []T, idOf, nameOf func(T) string) (T, error) {
	var zero T
	var matches []T
	for _, item := range items {
		if identifier == idOf(item) || identifier == nameOf(item) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return zero, errors.New("no match")
	}
	if len(matches) > 1 {
		return zero, errors.New("name is ambiguous; use the exact ID")
	}
	return matches[0], nil
}

func validAutomationUTCTime(value string) bool {
	if len(value) != 5 || value[2] != ':' {
		return false
	}
	if value[0] < '0' || value[0] > '2' || value[1] < '0' || value[1] > '9' || value[3] < '0' || value[3] > '5' || value[4] < '0' || value[4] > '9' {
		return false
	}
	return value[:2] < "24"
}
