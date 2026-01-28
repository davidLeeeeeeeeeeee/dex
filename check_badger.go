package main

import (
	"fmt"
	"reflect"

	"github.com/dgraph-io/badger/v4"
)

func main() {
	opts := badger.DefaultOptions("")
	t := reflect.TypeOf(opts)
	for i := 0; i < t.NumField(); i++ {
		fmt.Println(t.Field(i).Name)
	}
}
