package runtime

import "testing"

func TestNativePTYStateThreeModesKeepsEnvironmentAndConfirmedProcessIdentity(t *testing.T) {
	for _, mode := range []string{"conversation", "background", "stateful"} {
		t.Run(mode, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, mode)
			engine := newNativePTYEngine(t)
			manager := nativePTYManagerForTest(t, store, engine)
			absent, err := manager.ReadPTYState(ctx, access)
			if err != nil || absent.State != "absent" || absent.Processes == nil || engine.creates != 0 {
				t.Fatalf("absent state = %+v, %v", absent, err)
			}
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			request := nativePTYCall(t, store, ctx, "inventory-start", nil, true)
			started, err := manager.ExecutePTY(ctx, access, request)
			if err != nil {
				t.Fatal(err)
			}
			state, err := manager.ReadPTYState(ctx, access)
			if err != nil || state.State != "ready" || state.SessionID != access.SessionID || len(state.Processes) != 1 {
				t.Fatalf("ready state = %+v, %v", state, err)
			}
			process := state.Processes[0]
			if process.PTYSessionID != started.PTYSessionID || process.ProcessID != started.Result.ProcessID || process.Sequence != 1 || !process.TTY || process.Status != "running" {
				t.Fatalf("process = %+v", process)
			}
			if state.EnvironmentID != nativeEnvironmentForTest(t, store, access).environmentID {
				t.Fatal("inventory switched environment")
			}
			if _, err := manager.TerminatePTYs(ctx, access); err != nil {
				t.Fatal(err)
			}
			ended, err := manager.ReadPTYState(ctx, access)
			if err != nil || len(ended.Processes) != 1 || ended.Processes[0].Status != "exited" || ended.Processes[0].Sequence != 1 {
				t.Fatalf("ended state = %+v, %v", ended, err)
			}
			if _, err := manager.Shutdown(ctx, access); err != nil {
				t.Fatal(err)
			}
			closed, err := manager.ReadPTYState(ctx, access)
			if err != nil || closed.State != "closed" || closed.EnvironmentID != state.EnvironmentID || len(closed.Processes) != 0 || closed.Processes == nil {
				t.Fatalf("closed state = %+v, %v", closed, err)
			}
			if engine.starts.Load() != 1 || engine.inputs.Load() != 1 {
				t.Fatal("reading inventory executed process I/O")
			}
		})
	}
}

func TestNativePTYStateRejectsUnknownOrConcurrentBindings(t *testing.T) {
	for _, fault := range []string{"lost", "starting", "operation", "blocked", "lease"} {
		t.Run(fault, func(t *testing.T) {
			store, _, ctx, access := nativeWorkspaceFixture(t, "conversation")
			engine := newNativePTYEngine(t)
			manager := nativePTYManagerForTest(t, store, engine)
			if _, err := manager.Ensure(ctx, access); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ExecutePTY(ctx, access, nativePTYCall(t, store, ctx, "state-start", nil, true)); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE native_workspace_pty_processes SET status=? WHERE session_id=?`
			value := fault
			switch fault {
			case "operation":
				query, value = `UPDATE native_workspace_environments SET operation_id=? WHERE session_id=?`, "pending"
			case "blocked":
				query = `UPDATE native_workspace_environments SET status=? WHERE session_id=?`
			case "lease":
				query, value = `UPDATE native_workspace_leases SET lease_until=? WHERE session_id=?`, "2000-01-01T00:00:00Z"
			}
			if _, err := store.db.Exec(query, value, access.SessionID); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.ReadPTYState(ctx, access); err == nil {
				t.Fatal("unknown process state was advertised as recoverable")
			}
			if engine.starts.Load() != 1 || engine.inputs.Load() != 0 || engine.exports != 0 || engine.deletes != 0 {
				t.Fatal("invalid state recovery performed engine effects")
			}
		})
	}
}
