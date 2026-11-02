package identity

import "testing"

func TestSyslogTag(t *testing.T) {
	for _, test := range []struct {
		project string
		want    string
	}{
		{project: "contract-listener", want: "contract-listener"},
		{project: "项目-v2", want: "项目-v2"},
		{project: "a b\t\n\x1b", want: "a_b___"},
		{project: "a\u00a0b\u2003c\u0085d", want: "a_b_c_d"},
		{project: "a:b[c]=+", want: "a:b[c]=+"},
		{project: "", want: ""},
	} {
		if got := SyslogTag(test.project); got != test.want {
			t.Fatalf("项目协议标识错误：project=%q got=%q want=%q", test.project, got, test.want)
		}
	}
}
