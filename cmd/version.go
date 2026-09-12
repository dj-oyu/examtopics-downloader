package main

import "fmt"

// runVersionCmd implements `examtopicsdl version`. The legacy
// `-version` flag in main()'s body keeps working too, retained for
// scripts written against the pre-subcommand interface.
func runVersionCmd(_ []string) int {
	fmt.Println(version)
	return 0
}
