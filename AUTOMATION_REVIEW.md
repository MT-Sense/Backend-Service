# ตรวจงาน HR Semi Automation — branch dev

วันที่เพิ่ม: 2026-09-27 ทุก repository อยู่บน branch `dev` และยังไม่มีการ commit

## ตรวจการรวมกับ commit Knowledge Base / Q&A

- เลื่อน `dev` แบบ fast-forward ให้รวม Backend `16cd0dd`, AI-Service `90efbba`, Frontend `97ab838`
- นำ `stash@{0}` ชื่อ `pre-llm-summary-kb-2026-09-27` กลับมาครบทั้งสาม repo โดยเก็บ stash เดิมไว้ ยังไม่มีการ stage หรือ commit เพิ่ม
- ไม่มี merge conflict; ตรวจจุดร่วมใน models, routes, AI app และคำแปลแล้ว ทั้ง Knowledge Base/Q&A และผู้ช่วย HR ยังลงทะเบียนครบ
- หลังรวม: Backend tests ผ่าน รวม integration ของ Automation และ Knowledge Base, AI unittest ผ่าน 26 tests, Frontend type-check/build ผ่าน
- Knowledge Base integration test ใช้ fixture ID ตายตัว จึงต้องใช้ฐานทดสอบว่างแยกจากฐานที่มี seed และสร้างฐานใหม่ก่อนรันทดสอบซ้ำ
- ผู้ช่วย HR ยังอ่าน survey/Jira/เอกสารใน Settings ตามเดิม ไม่ได้ดึงบทความ Knowledge Base ใหม่เข้า prompt โดยอัตโนมัติ

## ลองบนเว็บทดสอบที่เปิดไว้

- URL: http://localhost:5175/hr-assistant
- บัญชีจำลอง HR: `hr@mtsense.local` / `password123`
- Frontend พอร์ต 5175 → Backend พอร์ต 8081 → PostgreSQL ทดสอบเฉพาะเครื่อง พอร์ต 55439
- ฐานข้อมูลนี้แยกจาก Neon/ข้อมูลบริษัทจริง และใช้ข้อมูล seed ของโครงการ
- หน้า preview นี้เปิดสำหรับลอง demo; ยังไม่ได้ตั้ง service token หรือ credential Jira
- หาก process ทดสอบหยุดแล้ว ใช้วิธีรันปกติด้านล่างได้

## สิ่งที่เพิ่ม

1. Sidebar **ผู้ช่วย HR** (`/hr-assistant`) สำหรับผู้ใช้ role `admin` ซึ่งใน UI เรียก HR
2. Playbook 4 เรื่อง: workload, approvals, training, benefits
3. **Settings → ตั้งค่าผู้ช่วย HR**: เกณฑ์จำนวน/สัดส่วน, Jira Cloud, Department → Project, เอกสารหลักสูตร/ขั้นอนุมัติ/สวัสดิการ
4. อ่านข้อมูลสำรวจที่วิเคราะห์แล้วจาก `response_analysis.categories`, สรุปจำนวนโดยองค์กร/รอบ/แผนก และส่งตัวอย่างข้อความที่ผ่าน redaction ให้ AI
5. ใช้ Gemini สร้างร่างแบบมีโครงสร้าง ตรวจ evidence IDs และจำนวนผลลัพธ์ ไม่สร้างร่างเมื่อ AI ไม่พบปัญหาเฉพาะเรื่อง
6. อ่าน Jira ที่ได้รับอนุญาตสำหรับ workload/approvals แล้วนำสถิติประกอบให้ AI; ไม่มี credential อยู่ใน prompt
7. HR แก้ไข/อนุมัติ/ปฏิเสธ/ถอนอนุมัติได้ งานภายในมีขั้นเริ่มงานและบันทึกผลเสร็จ
8. เปลี่ยนปลายทางเป็น Jira Task ได้หลังตั้งค่าและอนุญาตเขียน การอนุมัติและการกดดำเนินการเป็นคนละขั้น
9. บันทึกประวัติ snapshot ของร่างทุกขั้น และตรวจ version ป้องกันอนุมัติร่างเก่า
10. Demo 4 เรื่องที่ระบุชัดว่าเป็นข้อมูลจำลอง และห้ามดำเนินการกับ Jira

## ขั้นตอนตรวจโดยไม่ต้องมี Jira หรือ Gemini

1. Login เป็น HR → เมนู **ผู้ช่วย HR** → **ลองตัวอย่าง 4 เรื่อง**
2. ตรวจว่ามี 4 รายการและทุกใบติดป้าย **ข้อมูลจำลอง**
3. เปิดหลักฐานและอ่านข้อมูลที่ยังขาด
4. เลือกหนึ่งใบ → **แก้ไขร่าง / ปลายทาง** → เปลี่ยนรายละเอียด → **บันทึกร่าง**
5. **อนุมัติ** → ตรวจข้อความยืนยัน → **ยืนยัน**
6. สถานะเป็นอนุมัติแล้ว แต่ยังไม่มีการดำเนินการ; ปุ่มแก้ไขหายไป
7. ลอง **ถอนอนุมัติเพื่อแก้ไข** แล้วอนุมัติใหม่
8. **เริ่มงานติดตาม** → ยืนยัน → **บันทึกผลและปิดงาน** → ใส่ผล → ยืนยัน
9. ตรวจสถานะเสร็จแล้วและ **ประวัติการตรวจและดำเนินการ**
10. อีกใบลอง **ปฏิเสธ** และตรวจว่าไม่มีปุ่มดำเนินการ
11. รีเฟรชหน้า ข้อมูลและประวัติต้องอยู่ครบ
12. Settings: ลองเปลี่ยนเกณฑ์หรือเอกสารตัวอย่าง บันทึกแล้วโหลดใหม่ต้องอยู่ครบ

## รันกับข้อมูลโครงการจริง

Backend จะสร้างตารางใหม่ด้วย AutoMigrate เมื่อเริ่มเซิร์ฟเวอร์:
`automation_settings`, `automation_proposals`, `automation_events`
ไม่มีการแก้ไขย้อนหลังใน `survey_responses` หรือ `response_analysis` จาก workflow นี้

1. ใน `Backend-Service/.env` เพิ่ม:

```dotenv
AUTOMATION_ENCRYPTION_KEY=<base64 ของ random 32 bytes>
AUTOMATION_SERVICE_TOKEN=<ค่าลับสุ่มสำหรับ Backend เรียก AI-Service>
```

สร้างค่าสุ่มแยกกันด้วย `openssl rand -base64 32` สองครั้ง เก็บ encryption key เดิมไว้เพื่อถอดรหัส Jira token ที่บันทึกแล้ว ห้าม commit ค่าเหล่านี้

2. ใน `AI-Service/.env` เพิ่ม service token ค่าเดียวกับ Backend:

```dotenv
AUTOMATION_SERVICE_TOKEN=<ค่าเดียวกับ Backend>
# เลือกโมเดลอื่นที่รองรับ structured output ได้ ถ้าไม่กำหนดจะใช้ LLM_MODEL_NAME
# AUTOMATION_MODEL=...
```

`GEMINI_API_KEY` ใช้ค่าที่โปรเจกต์มีอยู่แล้ว `.env` ของคุณยังไม่ถูกแก้ในการทำงานครั้งนี้

3. รันแต่ละส่วนใน terminal ของตัวเอง:

```sh
cd /Users/win/Documents/SP/AI-Service
venv/bin/python -m uvicorn app.main:app --host 127.0.0.1 --port 8000
```

```sh
cd /Users/win/Documents/SP/Backend-Service
go run ./cmd/server
```

```sh
cd /Users/win/Documents/SP/Web-Frontend
pnpm run dev
```

4. Login ด้วยบัญชี HR เดิมของคุณ เช่น `test@kmitl.ac.th` แล้วเปิด **Settings → ตั้งค่าผู้ช่วย HR**
5. ใส่เอกสารหลักสูตร ขั้นตอนอนุมัติ หรือสวัสดิการ โดยระบุชื่อเอกสารและวันที่มีผล
6. เปิด **ผู้ช่วย HR** เลือกรอบ/แผนก → **วิเคราะห์และเตรียมข้อเสนอ**
7. ค่าเริ่มต้นคือกลุ่มมีอย่างน้อย 5 คำตอบที่วิเคราะห์แล้ว และแต่ละหัวข้อมีอย่างน้อย 5 คำตอบ/สัดส่วนอย่างน้อย 30%
8. ไม่มีข้อเสนอใหม่ได้เมื่อ: ไม่ผ่านเกณฑ์, เป็นคำชม, ไม่พบปัญหาเฉพาะ playbook หรือมีข้อเสนอหัวข้อนี้ในรอบและกลุ่มเดียวกันแล้ว
9. หากไม่มี Jira/เอกสาร จะระบุข้อมูลที่ขาดและเสนอการรวบรวมข้อมูลได้ ไม่สร้างข้อเท็จจริงทดแทน

## ตรวจ Jira จริง

- รองรับ Jira Cloud site รูปแบบ `https://company.atlassian.net` เท่านั้น
- เวอร์ชันนี้ใช้ Email + API token **แบบไม่มี scopes** กับ site URL โดยตรง ยังไม่มี OAuth หรือ scoped-token gateway
- ตั้ง Project key ส่วนกลาง เช่น `HR`, Issue type ID ที่มีจริงใน Project เช่น `10001` (ต้องตรวจค่าของบริษัท ไม่ใช่ค่าตายตัว)
- หากวิเคราะห์เป็นรายแผนก ให้ตั้ง Department → Project; ระบบไม่ใช้ Project ส่วนกลางแทนแผนกที่ยังไม่จับคู่
- บันทึกแล้วกด **ทดสอบการเชื่อมต่อที่บันทึกแล้ว** ทดสอบสิทธิ์อ่าน Project เท่านั้น
- เก็บ token แบบ AES-256-GCM โดยมี org ID เป็น authenticated data, ไม่ส่ง token กลับ client, ไม่ใช้ JWT secret เป็น encryption key
- อ่านเฉพาะ key/status/duedate ของงานที่ยังไม่เสร็จสูงสุด 100 งาน พร้อม timestamp และ flag ว่าครบหรือเป็น sample ไม่อ่านคำอธิบายลูกค้า/ไฟล์/assignee
- หลังเปิดอนุญาตเขียน: สร้างข้อเสนอจากข้อมูลจริง → แก้ร่างเลือก **สร้าง Task ใน Jira** → บันทึก → ตรวจ Site/Project/Issue type/ชื่อ/รายละเอียด → อนุมัติ → **สร้าง Task ใน Jira** → ยืนยัน
- Jira ได้เฉพาะชื่อและรายละเอียดที่ HR ตรวจ พร้อม label `mtsense-<proposal-id>`; ระบบไม่แนบ evidence หรือความคิดเห็นสำรวจอัตโนมัติ
- การสร้าง Task ไม่ได้ย้ายงาน/เปลี่ยนผู้รับผิดชอบ/เผยแพร่ FAQ/ส่งอีเมล หรือทำการอนุมัติงบแทน HR
- หาก Jira ตอบกลับไม่ชัดเจน สถานะเป็น `needs_check`; หาก process หยุดระหว่างส่งอาจค้าง `executing` ให้ตรวจ Jira ด้วย label ดังกล่าว ระบบไม่ resend อัตโนมัติ
- เปลี่ยน Settings หลังอนุมัติแล้ว: ต้องถอนอนุมัติ → บันทึกร่างเพื่อรับปลายทางและ settings revision ปัจจุบัน → อนุมัติใหม่

## สถานะและข้อจำกัดที่ต้องรู้

- เป็น bounded workflow: Backend เลือกเครื่องมืออ่านตาม playbook; Gemini ประเมินหลักฐานและร่างข้อเสนอ ไม่มี LLM tool loop ที่เปิดให้เรียก API ใดก็ได้
- เริ่มด้วยปุ่มให้ HR สั่งรัน ยังไม่มี scheduler/auto-run หลังปิดรอบ
- ชุดหลักสูตรและคู่มือใช้ข้อความใน Settings ยังไม่มี connector LMS/Drive หรือการอ่านไฟล์ PDF
- approvals คัดจากหมวด manager และ AI ตรวจว่ามีปัญหาการอนุมัติจริง ไม่ครอบคลุมข้อความที่จัดอยู่เฉพาะ work ในเวอร์ชันนี้
- Jira เป็น snapshot ปัจจุบัน ไม่ใช่ข้อมูลย้อนหลังของเดือนที่เลือก ไม่มี changelog จึงยังสรุปเวลารออนุมัติจริงไม่ได้
- หนึ่งข้อเสนอต่อ org/period/department/playbook รวมสถานะ rejected/completed; ไม่เขียนทับประวัติด้วยการ generate ใหม่
- รายการแสดงล่าสุด 100 ใบ; ตัวเลือกรอบ/แผนกด้านบนกำหนดการวิเคราะห์ใหม่ ส่วนรายการใช้ตัวกรอง demo/live และสถานะ
- ข้อจำกัด privacy.Redact เดิมเป็น pattern-based ไม่รับประกันลบชื่อ/ข้อมูลระบุตัวตนได้ทุกกรณี ควรใช้ข้อมูลจำลองสำหรับการนำเสนอ และตรวจร่างก่อนส่งออก
- โมเดลยังอาจตีความผิดหรือเติมรายละเอียด แม้ตรวจ JSON และ evidence IDs ผ่าน ต้องให้ HR ตรวจเนื้อหาเสมอ
- ผลอนุมัติ/ปฏิเสธเป็นบันทึกแผน ไม่มีการตัดสินสิทธิ์ของพนักงานรายบุคคล

## ไฟล์หลักสำหรับ review

Backend:
- `internal/models/automation.go`: ตาราง settings/proposal/events
- `internal/handlers/automation.go`: ตั้งค่าและ state transitions
- `internal/handlers/automation_generate.go`: privacy gate, sources, demo และ dedupe
- `internal/automation/jira.go`: credential encryption + Jira connector
- `internal/automation/planner.go`: AI-Service contract และ validation
- `internal/router/automation_test.go`: integration tests กับ PostgreSQL

AI-Service:
- `app/automation.py`: prompt, schema, token auth และ validation
- `tests/test_automation.py`: auth/error/schema/citations tests

Frontend:
- `src/views/hr/HrAssistantView.vue`: หน้า review/approve/execute/history
- `src/components/forms/AutomationSettingsPanel.vue`: การตั้งค่า
- `src/api/automation.ts`: API contract

## ผลทดสอบ

- Backend `go test ./...` ผ่าน รวม workflow integration บน PostgreSQL ชั่วคราว
- ทดสอบ Jira ผ่าน HTTP transport จำลอง: payload ที่อนุมัติ, ไม่ส่งซ้ำ, ambiguous result และ settings revision guard ผ่าน
- AI-Service `venv/bin/python -m unittest discover -s tests`: 18 tests ผ่าน
- Frontend `pnpm run build`: type-check และ build ผ่าน
- Gemini จริง: ข้อความจำลอง 4 เรื่องได้ร่างครบ (ไม่ใช้ข้อมูล HR จริง)
- Browser: Login → demo → แก้ร่าง → อนุมัติ → เริ่มงาน → ปิดงาน → ประวัติ และบันทึก Settings ผ่าน
- ยังไม่ได้ทดสอบเชื่อมต่อหรือสร้าง Task กับ Jira บริษัทจริง เพราะยังไม่มี credential สำหรับ Jira

เอกสารอ้างอิง:
- https://developer.atlassian.com/cloud/jira/platform/basic-auth-for-rest-apis/
- https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/
- https://ai.google.dev/gemini-api/docs/structured-output

## ต้นแบบคัดแยกและส่งต่อที่ตั้งค่าได้

หน้า **ผู้ช่วย HR → ศูนย์คัดแยกและส่งต่อ** เพิ่มระบบแยกจาก workflow AI เดิม:

1. เปิด **ตั้งกฎหัวข้อ → แผนกผู้รับผิดชอบ** → เติมแม่แบบ 3 เรื่อง หรือเพิ่มกฎเอง
2. ใส่คำสำคัญคั่นด้วยจุลภาค เลือกแผนกในองค์กร แล้วบันทึกกฎ สามารถแก้ ลบ และปิดกฎได้
3. เปิดเรื่อง เช่น ชื่อ `ระบบล่มบ่อย` และสรุปที่ HR ตรวจแล้ว ถ้ามีกฎตรงเพียงหนึ่งกฎจะแนะนำหัวข้อ/แผนก แต่ยังรอตรวจเสมอ
4. ลองเรื่องที่ไม่ตรงกฎ หรือคำที่ตรงหลายกฎ จะคงสถานะรอคัดแยก ไม่เลือกผู้รับเอง
5. กดตรวจและอัปเดตเรื่อง ใส่หัวข้อใหม่ได้โดยไม่ต้องเพิ่มโค้ด เลือกผู้รับ บันทึกเหตุผล แล้วกดยืนยันบันทึกส่งต่อ
6. หลังประสานผู้รับจริง HR บันทึกว่ารับเรื่องแล้ว จากนั้นบันทึกผลและปิดเรื่อง หรือคืนคัดแยกเพื่อเปลี่ยนผู้รับ พร้อมประวัติทุกขั้น
7. รีเฟรชแล้วข้อมูลยังอยู่ ใช้ version ตรวจการแก้พร้อมกัน และจำกัดข้อมูลตามองค์กร/สิทธิ์ HR

ข้อจำกัดต้นแบบ:
- จับคำสำคัญจากชื่อ/สรุปที่กรอก ไม่ใช่ AI ค้นพบหัวข้อใหม่จาก survey อัตโนมัติ และไม่ได้แทน 4 playbook เดิม
- คัดลอกร่างข้อเสนอเดิมได้ แต่เป็นเรื่องอิสระ ไม่มีการซิงก์สถานะกลับหรือป้องกันเปิดเรื่องซ้ำ
- เป็นทะเบียนประสานงานสำหรับ HR ยังไม่มี inbox ผู้รับ การแจ้งเตือน อีเมล หรือการส่ง Jira จากกฎนี้ แผนกผู้รับยังไม่เห็นข้อมูลผ่านสิทธิ์พนักงานทั่วไป
- ปุ่มรับเรื่องเป็น HR บันทึกผลการประสาน ไม่ใช่หลักฐานว่าผู้รับกดยอมรับเอง
- การส่งต่อไม่ได้อนุมัติงบหรือการแก้ปัญหาใด ๆ
- กฎใหม่ใช้กับเรื่องใหม่ เรื่องเดิมไม่เปลี่ยนผู้รับย้อนหลัง และแสดงล่าสุด 100 เรื่อง
- เริ่ม Backend ใหม่เพื่อ AutoMigrate `routing_policies` และ `routing_cases`

## เครื่องมือและการเชื่อมต่อใน Settings

- หน้า Settings หลักแสดงเพียงการ์ดสรุป กดเข้า URL `/settings/hr-assistant` เพื่อเปิดการตั้งค่าผู้ช่วย HR ทั้งหมด หน้านี้จำกัดเฉพาะ role admin/HR และมีลิงก์กลับ Settings หลัก
- เปลี่ยนหัวข้อที่เดิมแสดง Jira ตลอดเวลาเป็น **เครื่องมือและการเชื่อมต่อ**
- HR กรอกชื่อเครื่องมือ/ช่องทางและข้อกำหนดการใช้ข้อมูลเองได้
- โหมด `Manual` ไม่รับ credential และไม่เรียก API เหมาะกับเครื่องมือหรือขั้นตอนภายในที่ยังไม่มี connector
- โหมด `Jira Cloud API` แสดงช่อง credential และ Project เดิมเฉพาะเมื่อ HR เลือก adapter นี้ ข้อมูลเดิมยังใช้ต่อได้
- ชื่อเครื่องมือเพียงอย่างเดียวไม่ทำให้ระบบเรียก API ของผลิตภัณฑ์นั้นได้ การเพิ่มระบบอื่นต้องทำ adapter สำหรับรูปแบบ auth/read/write ของระบบนั้นโดยเฉพาะ
- เมื่อใช้ Manual การวิเคราะห์จะแจ้งว่าไม่มีข้อมูล API ประกอบ แทนการเดาหรือแอบเรียก URL ที่ HR กรอก

ทดสอบ: Frontend type-check/build ผ่าน; Backend tests รวม routing integration (สิทธิ์ HR, ข้ามองค์กร, version, กฎไม่ตรง, เปลี่ยนผู้รับ, ปิดเรื่องและประวัติ) ผ่านบน PostgreSQL ทดสอบ ยังไม่ได้ทดสอบหน้าใหม่ผ่านเบราว์เซอร์
