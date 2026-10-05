package loadtest

import (
	"fmt"
	"net/http"
	"strings"
)

const FixtureVersion = "mixed-scraping-v1"

func workloadNames(name string) ([]string, error) {
	switch name {
	case "article", "feed", "dashboard":
		return []string{name}, nil
	case "mixed":
		return []string{"article", "feed", "dashboard"}, nil
	default:
		return nil, fmt.Errorf("workload must be article, feed, dashboard, or mixed")
	}
}
func (c Config) scenario(index int) (string, string) {
	if c.Workload == "" {
		return "custom", c.TargetURL
	}
	names, _ := workloadNames(c.Workload)
	name := names[index%len(names)]
	return name, strings.TrimRight(c.FixtureURL, "/") + "/" + name
}

// FixtureHandler serves controlled scraping targets. No user data or external websites are requested.
func FixtureHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("GET /records", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "[")
		for i := 0; i < 200; i++ {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id":%d,"title":"Benchmark post %d","body":"%s"}`, i, i, strings.Repeat("controlled article text ", 30))
		}
		fmt.Fprint(w, "]")
	})
	mux.HandleFunc("GET /image.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360"><rect width="640" height="360" fill="#38618c"/><text x="20" y="180" fill="white" font-size="24">Controlled benchmark image</text></svg>`)
	})
	for _, name := range []string{"article", "feed", "dashboard"} {
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, fixturePage(name))
		})
	}
	return mux
}
func fixturePage(name string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>Novel Bot ` + name + ` benchmark</title><style>body{font:16px system-ui;margin:20px;max-width:1100px}article{padding:12px;border-bottom:1px solid #ddd}img{width:320px;height:180px}.chart{width:640px;height:200px}</style></head><body><h1>Controlled ` + name + ` workload</h1><main></main><script>
const kind="` + name + `";
const records=fetch('/records').then(r=>{if(!r.ok)throw Error('fetch');return r.json()});
const main=document.querySelector('main');let page=0, ticks=0;
const text='Representative paragraphs with links, comments, and metadata. '.repeat(30);
function add(n){for(let i=0;i<n;i++){let a=document.createElement('article');a.dataset.record='true';a.innerHTML='<h2>Post '+(page++)+'</h2><a href="#">Controlled headline</a><p>'+text+'</p><img loading="lazy" src="/image.svg?post='+page+'">';main.appendChild(a)}}
const ready=records.then(data=>{
 if(kind==='article'){add(35);for(let i=0;i<100;i++){let p=document.createElement('p');p.textContent=text;main.appendChild(p)}}
 if(kind==='feed')add(60);
 if(kind==='dashboard'){
  add(100);let canvas=document.createElement('canvas');canvas.width=640;canvas.height=200;canvas.className='chart';main.prepend(canvas);
  // Bounded heap retained by the page; repeated decode/sort/render work simulates a SPA.
  window.retained=Array.from({length:8},()=>new Uint8Array(2*1024*1024).fill(7));
  setInterval(()=>{ticks++;let copy=Array.from({length:20000},(_,i)=>Math.sin(i+ticks)).sort((a,b)=>a-b);let ctx=canvas.getContext('2d');ctx.clearRect(0,0,640,200);for(let i=0;i<640;i++){ctx.fillStyle=i%2?'#38618c':'#52aa8a';ctx.fillRect(i,100,1,copy[i%copy.length]*80)};main.firstElementChild.dataset.tick=ticks},250);
 }
 return true;
});
window.novelbotBenchmark={version:'` + FixtureVersion + `',ready,step:async()=>{
 await ready;
 if(kind==='feed'){if(page<500)add(20);else{for(let i=0;i<20;i++)main.lastElementChild.remove();page-=20}}
 window.scrollTo(0,(window.scrollY+700)%(Math.max(1,document.body.scrollHeight-window.innerHeight)));
 let rows=[...main.querySelectorAll('[data-record]')];
 let extracted=rows.map(r=>({title:r.querySelector('h2').textContent,text:r.querySelector('p').textContent,url:r.querySelector('a').href}));
 let encoded=JSON.stringify(extracted);
 return rows.length>=35&&encoded.length>1000&&document.readyState==='complete';
}};
</script></body></html>`
}
