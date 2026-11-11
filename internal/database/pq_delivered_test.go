package database

import "testing"

// TestMarkPostQueueDelivered: the entries of the given message-ids are marked posted (and no
// longer in processing); other entries and unknown ids (relayed articles) are left alone, and a
// second call marks nothing again.
func TestMarkPostQueueDelivered(t *testing.T) {
	db := w0DB(t)
	_, groupID := adfOptPostQueueGroup(t)
	rows := adfOptPostQueueInsert(t, groupID, "pqdelivered", 3)
	var msgIDs []string
	for _, id := range rows {
		var m string
		if err := RetryableQueryRowScan(db.GetMainDB(), "SELECT message_id FROM post_queue WHERE id = ?", []interface{}{id}, &m); err != nil {
			t.Fatalf("message_id of row %d: %v", id, err)
		}
		msgIDs = append(msgIDs, m)
	}
	// row 1 is being processed by post-queue right now: delivery ends that too
	if _, err := RetryableExec(db.GetMainDB(), "UPDATE post_queue SET in_processing = 1 WHERE id = ?", rows[1]); err != nil {
		t.Fatal(err)
	}

	n, err := db.MarkPostQueueDelivered([]string{msgIDs[0], msgIDs[1], "<relayed@elsewhere.invalid>"})
	if err != nil {
		t.Fatalf("MarkPostQueueDelivered: %v", err)
	}
	if n != 2 {
		t.Errorf("marked %d entries, want 2", n)
	}
	for i, id := range rows {
		posted, inProcessing := adfOptPostQueueFlags(t, id)
		wantPosted := i < 2
		if posted != wantPosted || inProcessing {
			t.Errorf("row %d: posted=%v in_processing=%v, want posted=%v in_processing=false", i, posted, inProcessing, wantPosted)
		}
	}
	if n, err := db.MarkPostQueueDelivered(msgIDs[:2]); err != nil || n != 0 {
		t.Errorf("second call: marked %d, err %v, want 0, nil", n, err)
	}
	if n, err := db.MarkPostQueueDelivered(nil); err != nil || n != 0 {
		t.Errorf("empty call: marked %d, err %v, want 0, nil", n, err)
	}
}
