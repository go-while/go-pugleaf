package database

//import "sync"

type SanitizedArticleCache struct {
	//mux   sync.RWMutex
	Cache map[string]*SanitizedFields // (key: group name, value: expiry time
}

type SanitizedFields struct {
	GroupName string
}
