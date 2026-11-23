package sources

func init() {
	refuseWriteBack("vibe", "Vibe stores sessions in its own event-sourced store (a CURRENT pointer, generation manifests, pooled chunks and journal deltas) beside append-only messages.jsonl; write nothing back and run vibe directly instead of rewriting history.")
}
