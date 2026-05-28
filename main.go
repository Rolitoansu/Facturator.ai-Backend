package main

import (
	"fmt"
	"net/http"
)

func main() {
	fmt.Println("🚀 Servidor backend arrancando en el puerto 8080...")

	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pong"))
	})

	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		panic(err)
	}
}
