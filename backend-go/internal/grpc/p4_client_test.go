package grpc_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/truthtable/backend-go/api/audit/v1"
	"github.com/truthtable/backend-go/internal/grpc/enginetest"
)

// The P4 RPC wrappers (P4-PLAN §2.3i) carry the internal token, and the fake engine records
// every push.
func TestApplyRuntimeConfigIsRecordedWithTheInternalToken(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{})
	c := client(t, eng, time.Second, "s3cret")
	push := &pb.RuntimeConfig{Version: 7, Epoch: "e1", Judge: &pb.JudgeConfig{Provider: "openrouter", Credential: "openrouter"},
		Credentials: map[string]*pb.Credential{"openrouter": {Provider: "openrouter", AllowedHosts: []string{"https://openrouter.ai"}}}}
	res, err := c.ApplyRuntimeConfig(context.Background(), push)
	if err != nil {
		t.Fatal(err)
	}
	if res.GetAppliedVersion() != 7 || res.GetAppliedEpoch() != "e1" || res.GetJudgeStatus() != "configured" {
		t.Errorf("response %v", res)
	}
	pushes := eng.Pushes()
	if len(pushes) != 1 || pushes[0].GetCredentials()["openrouter"].GetAllowedHosts()[0] != "https://openrouter.ai" {
		t.Errorf("pushes %v", pushes)
	}
	if md := eng.Metadata(); len(md) != 1 || len(md[0].Get("x-trustagent-internal")) != 1 {
		t.Errorf("metadata %v", md)
	}
}

func TestDraftAndRetrieveWithoutScriptsAreUnimplemented(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{})
	c := client(t, eng, time.Second, "")
	if _, err := c.DraftEvaluator(context.Background(), &pb.DraftEvaluatorRequest{Description: "d"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("DraftEvaluator: %v", err)
	}
	if _, err := c.RetrieveContext(context.Background(), &pb.RetrieveContextRequest{Query: "q"}, 50*time.Millisecond); status.Code(err) != codes.Unimplemented {
		t.Errorf("RetrieveContext: %v", err)
	}
}

func TestRetrieveContextHonoursTheKnowledgeDeadline(t *testing.T) {
	eng := enginetest.Start(t, &enginetest.Engine{RetrieveContextFunc: func(ctx context.Context, _ *pb.RetrieveContextRequest) (*pb.RetrieveContextResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	c := client(t, eng, time.Minute, "")
	start := time.Now()
	_, err := c.RetrieveContext(context.Background(), &pb.RetrieveContextRequest{Query: "q"}, 30*time.Millisecond)
	if status.Code(err) != codes.DeadlineExceeded || time.Since(start) > 5*time.Second {
		t.Errorf("RetrieveContext past its deadline: %v after %v", err, time.Since(start))
	}
}
