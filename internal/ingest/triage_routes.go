// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/leansii/earshot/internal/core"
)

// itemView is the API representation of a feedback-issue. It omits raw
// feedback; only the scrubbed body is exposed.
type itemView struct {
	ID                string `json:"id"`
	Category          string `json:"category"`
	Title             string `json:"title"`
	Summary           string `json:"summary"`
	ScrubbedBody      string `json:"scrubbed_body"`
	Status            string `json:"status"`
	FeedbackCount     int    `json:"feedback_count"`
	GitHubIssueNumber int    `json:"github_issue_number,omitempty"`
	GitHubIssueURL    string `json:"github_issue_url,omitempty"`
}

func toItemView(it core.Item) itemView {
	return itemView{
		ID: it.ID, Category: it.Category, Title: it.Title, Summary: it.Summary,
		ScrubbedBody: it.ScrubbedBody, Status: string(it.Status), FeedbackCount: it.FeedbackCount,
		GitHubIssueNumber: it.GitHubIssueNumber, GitHubIssueURL: it.GitHubIssueURL,
	}
}

// taskView is the API representation of a task.
type taskView struct {
	ID            string `json:"id"`
	ItemID        string `json:"item_id"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Status        string `json:"status"`
	TrackerRef    string `json:"tracker_ref,omitempty"`
	TrackerNumber int    `json:"tracker_number,omitempty"`
	TrackerURL    string `json:"tracker_url,omitempty"`
}

func toTaskView(t core.Task) taskView {
	return taskView{
		ID: t.ID, ItemID: t.ItemID, Title: t.Title, Description: t.Description, Status: string(t.Status),
		TrackerRef: t.TrackerRef, TrackerNumber: t.TrackerNumber, TrackerURL: t.TrackerURL,
	}
}

// handleListTriage lists feedback-issues for the project.
func (s *Service) handleListTriage(w http.ResponseWriter, r *http.Request) {
	project, _ := r.Context().Value(ctxProject).(core.Project)
	status := core.Status(r.URL.Query().Get("status")) // "" = all
	items, err := s.store.ListItems(r.Context(), project.ID, status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	views := make([]itemView, 0, len(items))
	for _, it := range items {
		views = append(views, toItemView(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views})
}

// itemInProject loads an item and enforces tenant isolation.
func (s *Service) itemInProject(r *http.Request) (core.Item, bool) {
	project, _ := r.Context().Value(ctxProject).(core.Project)
	it, err := s.store.Item(r.Context(), chi.URLParam(r, "id"))
	if err != nil || it.ProjectID != project.ID {
		return core.Item{}, false
	}
	return it, true
}

// taskInProject loads a task and enforces tenant isolation.
func (s *Service) taskInProject(r *http.Request) (core.Task, bool) {
	project, _ := r.Context().Value(ctxProject).(core.Project)
	t, err := s.store.Task(r.Context(), chi.URLParam(r, "id"))
	if err != nil || t.ProjectID != project.ID {
		return core.Task{}, false
	}
	return t, true
}

// handleListItemTasks lists the tasks of one feedback-issue.
func (s *Service) handleListItemTasks(w http.ResponseWriter, r *http.Request) {
	it, ok := s.itemInProject(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "feedback-issue not found")
		return
	}
	tasks, err := s.store.TasksForItem(r.Context(), it.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": taskViews(tasks)})
}

// handleListTasks lists a project's tasks, optionally filtered by status.
func (s *Service) handleListTasks(w http.ResponseWriter, r *http.Request) {
	project, _ := r.Context().Value(ctxProject).(core.Project)
	status := core.Status(r.URL.Query().Get("status"))
	if status == "" {
		status = core.StatusNeedsTriage
	}
	tasks, err := s.store.ListTasks(r.Context(), project.ID, status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": taskViews(tasks)})
}

func (s *Service) handleApproveTask(w http.ResponseWriter, r *http.Request) {
	if s.approver == nil {
		writeErr(w, http.StatusNotImplemented, "triage not enabled")
		return
	}
	t, ok := s.taskInProject(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	pushed, err := s.approver.Approve(r.Context(), t.ID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toTaskView(pushed))
}

func (s *Service) handleRejectTask(w http.ResponseWriter, r *http.Request) {
	if s.approver == nil {
		writeErr(w, http.StatusNotImplemented, "triage not enabled")
		return
	}
	t, ok := s.taskInProject(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.approver.Reject(r.Context(), t.ID); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": t.ID, "status": string(core.StatusRejected)})
}

func taskViews(tasks []core.Task) []taskView {
	out := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskView(t))
	}
	return out
}
