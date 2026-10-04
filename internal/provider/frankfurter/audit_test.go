package frankfurter
import("context";"errors";"io";"net/http";"net/http/httptest";"reflect";"testing";"time";"github.com/Shyyw1e/banco-plata-test-assignment/internal/usecase";"github.com/Shyyw1e/banco-plata-test-assignment/internal/domain")
func TestAuditRateLimited(t *testing.T){for _,status:=range []int{429,503,408,400,403}{for _,header:=range []string{"","12","garbage",time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)}{var pe *usecase.ProviderError;errors.As(statusError(status,parseRetryAfter(header,time.Now())),&pe);v:=reflect.ValueOf(pe).Elem().FieldByName("RateLimited");if !v.IsValid()||v.Bool()!=(status==429){t.Errorf("status %d missing or incorrect RateLimited",status)}}}}
func TestAuditProviderFields(t *testing.T){for _,body:=range []string{
`{"date":"2026-10-02","date":null,"base":"EUR","quote":"USD","rate":1.2}`,
`{"date":"2026-10-02","base":"EUR","base":null,"quote":"USD","rate":1.2}`,
`{"date":"2026-10-02","base":"EUR","quote":"USD","quote":null,"rate":1.2}`,
`{"date":"2026-10-02","base":"EUR","quote":"USD","rate":1.2,"rate":2}`,
`{"date":"2026-10-02","base":"EUR","quote":"USD","rate":1.2,"\u0064ate":"2026-10-02"}`,
}{t.Run(body,func(t *testing.T){s:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){io.WriteString(w,body)}));defer s.Close();c,_:=New(s.URL,time.Second);q,e:=c.Fetch(context.Background(),testPair);if q!=nil{t.Error("accepted ambiguous quote")};assertProvider(t,e,domain.CodeInvalidResponse,false)})}}
