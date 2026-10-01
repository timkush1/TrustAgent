package main

import "github.com/truthtable/backend-go/internal/proxy"

// knowledgeRetriever is the gate's knowledge-base retriever over the engine's RetrieveContext
// (P4-PLAN D65, GGW). Step-0 stub: none, so no request gets knowledge.
func (s *server) knowledgeRetriever(d serverDeps) proxy.Retriever {
	_ = d
	return nil
}
