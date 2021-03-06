package router

import (
	"github.com/gorilla/mux"
	"net/http"
	"paotui.sg/app/handler/tasks"
	"paotui.sg/app/handler/tasks/task_update"
	"paotui.sg/app/handler/tasks/tasks_enquiry"
)

func Task(s *mux.Router) *mux.Router {
	s.HandleFunc("/tasks/task", tasks.NewTask).Methods(http.MethodPost, http.MethodOptions)
	s.HandleFunc("/tasks/{id}", tasks_enquiry.TaskEnquiry).Methods(http.MethodGet, http.MethodOptions)
	s.HandleFunc("/tasks/task/{taskID}", task_update.TaskUpdate).Methods(http.MethodPut, http.MethodDelete, http.MethodOptions)
	return s
}
