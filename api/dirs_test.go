package api

import (
	"encoding/json"
	"testing"
)

func TestDirectoryPatch(t *testing.T) {
	var patch DirPolicyPatch
	if err := json.Unmarshal([]byte(`{"index":"on","hidden":false,"noindex":null,"title":""}`), &patch); err != nil {
		t.Fatal(err)
	}
	if !patch.Index.HasValue() || patch.Index.Value != IndexOn || !patch.Hidden.HasValue() || patch.Hidden.Value || !patch.NoIndex.Null || patch.Description.Set {
		t.Fatal(patch)
	}
	b, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"hidden":false,"index":"on","noindex":null,"title":""}` {
		t.Fatal(string(b))
	}
	for _, bad := range []string{`null`, `[]`, `{"prefix":"a/"}`, `{"hidden":"yes"}`, `{"title":1}`} {
		if err := json.Unmarshal([]byte(bad), &patch); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if DirRoute("") != "/api/v1/dirs/" || DirRoute("space dir/") != "/api/v1/dirs/space%20dir/" {
		t.Fatal("bad directory route")
	}
}
