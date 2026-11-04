"""Generate public-script narration via Edge TTS; no API key or local weights."""
import asyncio,json,pathlib,subprocess
import edge_tts
out=pathlib.Path(__file__).parent/'output';out.mkdir(exist_ok=True)
async def main():
 story=json.loads((out.parent/'story.json').read_text())
 for i,scene in enumerate(story):
  target=out/f'voice-{i}.mp3'
  await edge_tts.Communicate(scene['voice'],'en-US-AriaNeural',rate='-3%').save(str(target))
  scene['duration']=float(subprocess.check_output(['ffprobe','-v','error','-show_entries','format=duration','-of','csv=p=0',str(target)]))+1.2
 (out/'timing.json').write_text(json.dumps(story))
asyncio.run(main())
