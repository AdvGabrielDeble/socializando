from pathlib import Path
p=Path('_gd_build/acceptance.py')
s=p.read_text(encoding='utf-8')
old="m = re.search(r'<option value=\"(\\d+)\"[^>]*>'+re.escape(name)+r'\\s+—', html)"
new="m = re.search(r'<option value=\"(\\d+)\"[^>]*>\\s*'+re.escape(name)+r'[^<]*</option>', html, re.I)"
if old not in s:
    raise SystemExit('expected regex not found')
p.write_text(s.replace(old,new),encoding='utf-8')
