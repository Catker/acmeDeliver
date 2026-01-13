package handler

import (
	"net/http"
)

// HandleHome 首页健康检查
func HandleHome(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("Running"))
}
