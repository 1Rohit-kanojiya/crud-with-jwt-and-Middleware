package main

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------- 1. DATA MODEL ----------

type Todo struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

// What the client is allowed to send us (they must not choose ID or CreatedAt).
type todoInput struct {
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// ---------- 2. IN-MEMORY STORAGE ----------

type TodoAPI struct {
	mu     sync.RWMutex
	todos  map[int]Todo
	nextID int
}

func NewTodoAPI() *TodoAPI {
	return &TodoAPI{
		todos:  make(map[int]Todo),
		nextID: 1,
	}
}

// ---------- 3. HELPERS ----------

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("writeJSON error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func getID(r *http.Request) (int, error) {
	return strconv.Atoi(r.PathValue("id"))
}

// ---------- 4. HANDLERS (one per CRUD operation) ----------

// CREATE: POST /todos
func (a *TodoAPI) createTodo(w http.ResponseWriter, r *http.Request) {
	var in todoInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		writeError(w, http.StatusUnprocessableEntity, "title is required")
		return
	}

	a.mu.Lock()
	todo := Todo{ID: a.nextID, Title: in.Title, Done: in.Done, CreatedAt: time.Now()}
	a.todos[todo.ID] = todo
	a.nextID++
	a.mu.Unlock()

	slog.Info("todo created", "request_id", RequestIDFrom(r.Context()), "todo_id", todo.ID)
	writeJSON(w, http.StatusCreated, todo)
}

// READ (all): GET /todos
func (a *TodoAPI) listTodos(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	list := make([]Todo, 0, len(a.todos))
	for _, t := range a.todos {
		list = append(list, t)
	}
	a.mu.RUnlock()

	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	writeJSON(w, http.StatusOK, list)
}

// READ (one): GET /todos/{id}
func (a *TodoAPI) getTodo(w http.ResponseWriter, r *http.Request) {
	id, err := getID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a number")
		return
	}

	a.mu.RLock()
	todo, ok := a.todos[id]
	a.mu.RUnlock()

	if !ok {
		writeError(w, http.StatusNotFound, "todo not found")
		return
	}
	writeJSON(w, http.StatusOK, todo)
}

// UPDATE: PUT /todos/{id}
func (a *TodoAPI) updateTodo(w http.ResponseWriter, r *http.Request) {
	id, err := getID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a number")
		return
	}

	var in todoInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		writeError(w, http.StatusUnprocessableEntity, "title is required")
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	todo, ok := a.todos[id]
	if !ok {
		writeError(w, http.StatusNotFound, "todo not found")
		return
	}
	todo.Title = in.Title
	todo.Done = in.Done
	a.todos[id] = todo

	writeJSON(w, http.StatusOK, todo)
}

// DELETE: DELETE /todos/{id}
func (a *TodoAPI) deleteTodo(w http.ResponseWriter, r *http.Request) {
	id, err := getID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "id must be a number")
		return
	}

	a.mu.Lock()
	_, ok := a.todos[id]
	delete(a.todos, id)
	a.mu.Unlock()

	if !ok {
		writeError(w, http.StatusNotFound, "todo not found")
		return
	}
	w.WriteHeader(http.StatusNoContent) // 204: success, nothing to return
}

// ---------- 5. ROUTER + SERVER ----------

func main() {
	// Set up structured logging once. Every slog.Info(...) call now uses this.
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	api := NewTodoAPI()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /todos", api.createTodo)
	mux.HandleFunc("GET /todos", api.listTodos)
	mux.HandleFunc("GET /todos/{id}", api.getTodo)
	mux.HandleFunc("PUT /todos/{id}", api.updateTodo)
	mux.HandleFunc("DELETE /todos/{id}", api.deleteTodo)

	// TEMPORARY: a route that panics, only to prove Recover works. Remove later.
	mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) {
		panic("something went terribly wrong")
	})

	// Wrap the whole router. First listed = outermost layer.
	handler := Chain(mux, RequestID, Logging, Recover)

	srv := &http.Server{
		Addr:              ":8080",
		Handler:           handler, // <-- was "mux" in Step 1
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("server starting", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}