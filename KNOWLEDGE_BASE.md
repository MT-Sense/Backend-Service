# MT-Sense LLM Knowledge Base

แนวทางนี้ดัดแปลงจาก Andrej Karpathy — LLM Knowledge Bases / LLM Wiki:

- X: https://x.com/karpathy/status/2039805659525644595
- Idea file: https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f

แกนสำคัญที่นำมาใช้คือ compile ความรู้ให้สะสมและเชื่อมโยงกัน แทนการค้น source เดิมใหม่ทุกครั้ง
โดยมี index, brief summaries และ backlinks เพื่อให้ Q&A ในอนาคตอ่านฐานความรู้ที่ผ่านการสังเคราะห์แล้ว

## การปรับให้เหมาะกับ MT-Sense

Karpathy ใช้ raw directory → LLM → Markdown wiki บน filesystem. MT-Sense เป็นระบบหลายองค์กรและมีข้อมูล
ความคิดเห็นพนักงานที่อ่อนไหว จึงไม่เขียน raw feedback ลง wiki หรือ filesystem

MT-Sense ใช้ flow นี้แทน:

~~~text
survey_responses + response_analysis
        |
        | aggregate + privacy filter (n >= 5)
        v
privacy-safe evidence snapshot
        |
        | POST AI-Service /knowledge/compile
        v
Gemini knowledge compiler
        |
        v
knowledge_base_summaries
  - summary TH/EN
  - Markdown article
  - tags
  - related period IDs / backlinks
  - suggested questions
  - source hash
        |
        +--> GET /api/knowledge-base
        +--> GET /api/knowledge-base/index
        +--> GET /api/knowledge-base/:periodId
~~~

ไม่มี raw employee comment อยู่ใน request ของ knowledge compiler โดยโครงสร้าง type เลย

## Evidence ที่อนุญาตเข้า compiler

- จำนวนคำตอบรวม เมื่อทั้งรอบมีอย่างน้อย 5 คำตอบ
- average satisfaction ขององค์กร
- sentiment split เฉพาะเมื่อมีอย่างน้อย 5 analyzed comments
- topic aggregate เฉพาะ topic ที่มีอย่างน้อย 5 responses
- department aggregate เฉพาะ department ที่มีอย่างน้อย 5 responses
- summary ของ knowledge articles รอบก่อนหน้า สูงสุด 5 รอบ

ไม่ส่ง comment text, sample quote, user id, email, full name, submission records รายบุคคล หรือ aggregate ที่ต่ำกว่า n=5

## Incremental compilation

ทุก compile request ถูก serialize แล้ว hash ด้วย SHA-256 เป็น source_hash

- source เดิม + previous knowledge เดิม → reuse article เดิม ไม่เรียก LLM ซ้ำ
- source เปลี่ยน → compile ใหม่และ update article เดิมของ period
- force=true → compile ใหม่แม้ hash ไม่เปลี่ยน

จึงมี article เดียวต่อ (org_id, period_id) และสามารถ rerun ได้โดยไม่สร้าง duplicate

## Backlinks และ index

Compiler ได้ summary ของรอบก่อนหน้าและคืน related_period_ids
Backend filter อีกครั้งให้เหลือเฉพาะ period ที่ส่งเข้า compiler จริง

GET /api/knowledge-base/index สร้าง Markdown index เช่น:

~~~md
# MT-Sense Knowledge Base

- [[2026-09]] — สรุปผลสำรวจบรรยากาศองค์กรประจำรอบ 2026-09
  - ...
- [[2026-08]] — สรุปผลสำรวจบรรยากาศองค์กรประจำรอบ 2026-08
  - ...
~~~

Markdown article จาก LLM สามารถใช้ [[YYYY-MM]] อ้างรอบที่เกี่ยวข้องได้

## Lifecycle

เมื่อ HR ปิด survey period:

1. ปิดรอบตาม flow เดิม
2. generate alerts
3. ลอง compile knowledge article
4. ถ้า AI-Service ใช้งานไม่ได้ การปิดรอบยังสำเร็จ
5. HR retry ได้ด้วย POST /api/knowledge-base/:periodId/compile

Dashboard HR จะใช้ LLM summary จาก knowledge article เมื่อมีอยู่
แต่ KPI, urgent issues และ aggregate อื่นยังคำนวณด้วย query deterministic ตามเดิม

## API

HR/Admin:

~~~text
POST /api/knowledge-base/:periodId/compile
POST /api/knowledge-base/:periodId/compile?force=true
~~~

HR/Admin + Executive:

~~~text
GET /api/knowledge-base
GET /api/knowledge-base/index
GET /api/knowledge-base/:periodId
~~~

Employee ไม่มีสิทธิ์อ่าน Knowledge Base

## สิ่งที่ยังไม่ทำใน branch นี้

Q&A ยังไม่ถูก implement โดยตั้งใจ

ขั้นต่อไปสามารถทำ Q&A แบบ wiki-first:

1. อ่าน index
2. เลือก article ที่เกี่ยวข้องจาก title/tags/summary/backlinks
3. โหลด Markdown article จำนวนเล็กน้อย
4. ตอบโดยอ้างเฉพาะ evidence ใน KB
5. ถ้าคำตอบสร้าง insight ใหม่ที่มีประโยชน์ สามารถ file กลับเข้า KB ภายใต้ review policy

แนวนี้รักษาหลัก knowledge accumulates และยังไม่จำเป็นต้องเพิ่ม vector DB จนกว่าสเกลของ wiki จะใหญ่พอ
