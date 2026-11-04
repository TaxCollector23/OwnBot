import json,pathlib,subprocess
p=pathlib.Path(__file__).parent/'output';story=json.loads((p/'timing.json').read_text());timeline=json.loads((p/'timeline.json').read_text())
args=['ffmpeg','-y','-i',str(p/'capture.webm')]
for i in range(len(story)):args+=['-i',str(p/f'voice-{i}.mp3')]
filters=[]
for j,t in enumerate(timeline): filters.append(f'[{j+1}:a]adelay={round(t["start"]*1000)}:all=1[a{j}]')
filters.append(''.join(f'[a{i}]' for i in range(len(story)))+f'amix=inputs={len(story)}:normalize=0[a]')
def stamp(t):
 ms=round(t*1000);return f'{ms//3600000:02}:{ms//60000%60:02}:{ms//1000%60:02},{ms%1000:03}'
cues=[]
for i,t in enumerate(timeline):
 words=story[i]['voice'].split(); chunks=[' '.join(words[n:n+10]) for n in range(0,len(words),10)]
 for j,chunk in enumerate(chunks):
  start=t['start']+j*t['duration']/len(chunks);end=t['start']+(j+1)*t['duration']/len(chunks)
  cues.append(f'{len(cues)+1}\n{stamp(start)} --> {stamp(end)}\n{chunk}')
(p/'captions.srt').write_text('\n\n'.join(cues)+'\n')
args+=['-filter_complex',';'.join(filters),'-map','0:v','-map','[a]','-vf',f"subtitles={p/'captions.srt'}:force_style='FontSize=19,PrimaryColour=&H00171717,BackColour=&H00F8FDFF,BorderStyle=3,Outline=1,MarginV=18'",'-c:v','libx264','-preset','fast','-crf','22','-pix_fmt','yuv420p','-c:a','aac','-b:a','128k','-movflags','+faststart',str(p/'ownbot-explainer.mp4')]
subprocess.run(args,check=True)
subprocess.run(['ffprobe','-v','error','-show_entries','format=duration:stream=codec_name,width,height','-of','json',str(p/'ownbot-explainer.mp4')],check=True)
