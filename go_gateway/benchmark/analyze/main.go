// SPDX-License-Identifier: MIT
// Report HDR mean attribution; percentiles cannot be subtracted.
package main
import("compress/gzip";"encoding/json";"fmt";"os";"sort";hdr "github.com/HdrHistogram/hdrhistogram-go")
type quantiles struct{HDR *hdr.Snapshot `json:"hdr"`}
type run struct{Ack quantiles `json:"ack"`;Lag quantiles `json:"send_lag"`;After quantiles `json:"ack_after_send"`}
func analyze(path string)(map[string]any,error){
 f,err:=os.Open(path);if err!=nil{return nil,err};defer f.Close()
 z,err:=gzip.NewReader(f);if err!=nil{return nil,err};defer z.Close()
 var data struct{Runs []run `json:"runs"`;Error string `json:"error"`};if err=json.NewDecoder(z).Decode(&data);err!=nil{return nil,err};if data.Error!="" || len(data.Runs)==0{return nil,fmt.Errorf("failed or empty benchmark: %s",data.Error)}
 values:=map[string][]float64{}
 for _,r:=range data.Runs{for name,q:=range map[string]quantiles{"ack_mean_ns":r.Ack,"sender_lag_mean_ns":r.Lag,"post_send_ack_mean_ns":r.After}{if q.HDR==nil{return nil,fmt.Errorf("missing %s histogram",name)};values[name]=append(values[name],hdr.Import(q.HDR).Mean())}}
 result:=map[string]any{}
 for name,v:=range values{sort.Float64s(v);median:=v[len(v)/2];if len(v)%2==0{median=(v[len(v)/2-1]+median)/2};result[name]=map[string]float64{"median":median,"min":v[0],"max":v[len(v)-1]}}
 return result,nil
}
func main(){result:=map[string]any{};for _,path:=range os.Args[1:]{r,err:=analyze(path);if err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(1)};result[path]=r};data,err:=json.MarshalIndent(result,"","  ");if err!=nil{panic(err)};fmt.Println(string(data))}
