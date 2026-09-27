# MT-Sense — Backend Service

REST API สำหรับ [MT-Sense](../Frontend) — ระบบเก็บ feedback พนักงานแบบไม่ระบุตัวตน

**Stack:** Go 1.25 · GoFiber v2 · GORM · PostgreSQL

Schema หลัก (organizations/departments/positions/users/survey_periods/survey_submissions/
survey_responses/response_analysis/dashboard_metrics/position_scores/keywords_monthly/
alerts/knowledge_base_summaries) ตรงกับ SQL ที่กำหนดไว้เป๊ะ — สร้างผ่าน GORM AutoMigrate
บวก raw-SQL bootstrap เล็กๆ ใน `database.Bootstrap` สำหรับ `pgcrypto` extension, enum types
(`user_role`/`sentiment_label`/`alert_severity`/`alert_type`) และ `set_updated_at` trigger
ที่ GORM เองสร้างให้ไม่ได้ ตารางที่ไม่ได้อยู่ใน schema ที่กำหนด (feed, action items, AI
insight/urgent-issues/decision-items/word-cloud/topic-drilldown, refresh tokens, topics
taxonomy) เป็นตารางเสริมที่ผูกกับ `org_id`/`period_id` ตามแบบเดียวกัน ไม่ได้ไปแทนที่อะไรที่
กำหนดไว้

**Single-tenant สำหรับตอนนี้** — schema เป็น multi-tenant เต็มรูปแบบ (มี `organizations`) แต่
seed สร้างองค์กรเดียว และไม่มีหน้าจอเลือกบริษัทก่อน login ทุก query ผูกกับ `org_id` อยู่แล้ว
ดังนั้นการทำ multi-tenant จริงในอนาคตคือแค่ resolve `org_id` ต่อ request แทนที่จะใช้ค่าคงที่

---

## เริ่มใช้งาน

ต้องมี PostgreSQL รันอยู่ก่อน

```bash
createdb mtsense
cp .env.example .env
```

แก้ `.env` แล้วใส่ `JWT_SECRET` (บังคับ ต้องยาว ≥32 ตัว — เซิร์ฟเวอร์จะไม่ยอมสตาร์ทถ้าไม่มี):

```bash
openssl rand -base64 48
```

```bash
go run ./cmd/server
```

ข้อความในแบบสำรวจถูกส่งไปวิเคราะห์ที่ AI-Service ก่อนบันทึกคำตอบ เริ่ม AI-Service
ที่ `http://127.0.0.1:8000` ก่อน (ดู `../AI-Service/README.md`) หรือกำหนด
`AI_SERVICE_URL` ใน `.env` ของ Backend หากใช้พอร์ตอื่น Backend ส่งเฉพาะข้อความที่
ลบข้อมูลระบุตัวตนแล้ว และบันทึก sentiment, confidence, หมวดหัวข้อ และเหตุผลจาก AI-Service
ลง `response_analysis` ถ้า AI-Service ไม่พร้อม การส่งคำตอบที่มีข้อความจะตอบ 503
และไม่บันทึกคำตอบ; คำตอบที่ไม่มีข้อความยังส่งได้

ครั้งแรกจะ migrate + seed ข้อมูลตัวอย่างให้อัตโนมัติ (1 องค์กร, แผนก/ตำแหน่ง, 6 รอบสำรวจ —
5 รอบปิดแล้ว + 1 รอบเปิดอยู่ พร้อม response/analysis จริงทุกแถว)

### บัญชีทดลอง (มีเฉพาะตอน seed)

| Email | Password | Role |
|---|---|---|
| `hr@mtsense.local` | `password123` | admin (HR) |
| `exec@mtsense.local` | `password123` | executive |
| `employee@mtsense.local` | `password123` | employee |

> `user_role` enum คือ `employee`/`executive`/`admin` — `admin` คือ role เดิมที่เคยเรียกว่า
> HR สิทธิ์เหมือนเดิมทุกอย่าง แค่เปลี่ยนชื่อให้ตรงกับ schema ที่กำหนด

---

## กฎความเป็นส่วนตัว — บังคับที่ backend ไม่ใช่แค่ซ่อนที่ UI

นี่คือเหตุผลที่ระบบนี้มีอยู่ ถ้าจะแก้โค้ดที่กระทบข้อใดข้อหนึ่ง **ต้องคุยกันก่อน**

### 1. คำตอบไม่ผูกกับตัวบุคคล

ตาราง `survey_responses` **ไม่มี foreign key ไปที่ `users`** เลย — ไม่มีแม้แต่คอลัมน์
anonymous token เพิ่มเติมแบบเดิม เพราะ `id` ของแถว (UUID สุ่มใหม่ทุกครั้ง) ก็เพียงพอแล้วที่จะ
ไม่โยงกลับไปหาใครได้ และเป็นค่าเดียวที่คืนกลับไปเป็น receipt

เก็บ `department_id` กับ `position_id` ไว้เป็น *คุณลักษณะหยาบ* เพื่อให้แบ่งกลุ่มดูสถิติได้
ซึ่งปลอดภัยเพราะมีกฎ n<5 คุมอยู่ (ข้อ 2)

`user_id` ถูกใช้แค่ 2 อย่างตอน submit และไม่มีอันไหนถูกเขียนลง response:
- อ่านแผนก/ตำแหน่งของผู้ตอบ
- เขียน `survey_submissions` ว่า "ส่งแล้ว" (นับ response rate + กันตอบซ้ำ)

ทั้งหมดอยู่ใน transaction เดียว

### 2. n < 5 suppression — บังคับใน SQL

ทุก query ที่ `GROUP BY` แผนก/ตำแหน่ง มี `HAVING COUNT(*) >= 5`

**ด่านแรกอยู่ที่ database** — คะแนนของกลุ่มเล็กไม่ถูก select ออกมาตั้งแต่แรก จึงไม่มีสำเนาใน memory ให้เผลอส่งออกไป ส่วน `privacy.Suppress()` เป็นด่านที่สอง

ผลลัพธ์ที่ส่งออกไปตรงกับ type `Suppressible<T>` ของ frontend เป๊ะ:

```json
{"suppressed": true}
{"suppressed": false, "data": 3.9}
```

Seed จงใจใส่ทีม `innovation` ให้มีผู้ตอบแค่ 3 คน เพื่อให้ทดสอบเส้นทางนี้ได้จริง

### 3. PII redaction ก่อนเขียนลง DB

`privacy.Redact()` ตัดข้อมูลระบุตัวตนออก **ก่อน** persist — ข้อความดิบไม่เคยลงตาราง (ถ้ากรองตอนแสดงผล ของจริงจะยังนอนอยู่ใน DB ให้คนที่เข้าถึง query ได้อ่าน)

ครอบคลุม: อีเมล · เบอร์โทรไทย/สากล · เลขบัตรประชาชน 13 หลัก · รหัสพนักงาน · URL · @handle · คำนำหน้า+ชื่อ (ไทย/อังกฤษ)

### 4. Feed ต้องผ่าน 2 ด่าน

`WHERE opted_in = true AND published = true` — เป็น **WHERE clause ไม่ใช่ filter ตอนแสดงผล** โพสต์ที่ยังไม่ผ่านทั้งสองด่านไม่ถูก select เลย

- ด่าน 1: ผู้ตอบติ๊กยินยอมตอนกรอกฟอร์ม
- ด่าน 2: HR (admin) review แล้วกด publish

API ปฏิเสธการ publish โพสต์ที่เจ้าของไม่เคยยินยอม (403)

### 5. Executive ไม่มีทางเห็นข้อความดิบ

`/dashboard/executive/summary` ไม่แตะคอลัมน์ข้อความเลย และ `/dashboard/hr/topics/:id` (endpoint เดียวที่คืนข้อความตัวอย่าง) ปิดด้วย `RequireRole(admin)`

### 6. Submission log แยกตาราง

`survey_submissions` เก็บแค่ "ใครส่งแล้ว/ยังไม่ส่ง" ไม่มี key ร่วมกับ `survey_responses` — query ปกติ join ไม่ได้

### หมายเหตุ: หัวข้อ (topics) แบบไม่มีคำถามต่อหัวข้อ

Schema ที่กำหนดไม่มีคำถามแยกรายหัวข้อ (survey มีแค่ `satisfaction_score` + `comment_text`)
ดังนั้นคะแนนต่อหัวข้อ (heatmap column, radar axis, topic drill-down) คำนวณจาก
`response_analysis.categories` — แท็กหัวข้อที่ AI-Service ให้กับ comment แต่ละอัน — โดยถือ
ว่า "คะแนนของหัวข้อนี้ในแผนกนี้" = ค่าเฉลี่ย `satisfaction_score` ของคนที่ comment แตะหัวข้อนั้น
จึงเป็นการประมาณ ไม่ใช่ตัวเลขที่วัดตรง ๆ เหมือนแบบสอบถามเดิม Heatmap ขยายค่าจาก
`response_analysis.categories` โดยตรง ไม่อ่าน `feed_posts.hashtags` (ดู `internal/analytics/analytics.go`)

---

## API

Auth: `Authorization: Bearer <accessToken>` ทุก endpoint ยกเว้น login/refresh/health

### Public
| Method | Path |
|---|---|
| GET | `/health` |
| POST | `/api/auth/login` |
| POST | `/api/auth/refresh` |

### ทุก role
| Method | Path | |
|---|---|---|
| POST | `/api/auth/logout` | revoke refresh token ทุกอัน |
| GET/PATCH | `/api/settings/me` | |
| GET | `/api/settings/me/submissions` | ประวัติส่งรายรอบ (ไม่มีเนื้อหาคำตอบ) |
| GET | `/api/topics` · `/api/departments` · `/api/positions` | |
| GET | `/api/surveys/current` | รอบที่เปิดอยู่ตอนนี้ + ส่งไปแล้วหรือยัง |
| POST | `/api/surveys/current/responses` | ส่งแบบไม่ระบุตัวตน `{satisfactionScore, commentText, optedInToFeed, tags}` |
| GET | `/api/feed` | `?sort=popular\|newest&tag=&limit=&offset=` |
| POST | `/api/feed/:id/vote` | `{"direction": 1 \| -1}` |
| GET | `/api/summaries` · `/api/action-items` | |

### admin (HR) เท่านั้น
| Method | Path |
|---|---|
| GET | `/api/dashboard/hr/kpi` · `/heatmap` · `/wordcloud` · `/insight` · `/alerts` |
| GET | `/api/dashboard/hr/topics/:id` |
| GET/POST | `/api/survey-periods` | list / เปิดรอบใหม่ |
| POST | `/api/survey-periods/:id/close` | ปิดรอบ + คำนวณ alerts |
| GET | `/api/feed/pending` |
| PATCH | `/api/feed/:id/moderate` |
| GET/POST | `/api/hr/departments` | ดูรายชื่อและสร้าง Department ขององค์กร |
| PATCH/DELETE | `/api/hr/departments/:id` | เปลี่ยนชื่อหรือลบ Department ที่ยังไม่มีข้อมูลอ้างอิง |

HR เปิดหน้า `/departments` เพื่อสร้าง Department และใช้รหัส `organizations.join_code` จากหน้า Settings แจกพนักงาน
พนักงานกรอกรหัสบริษัทที่หน้า `/join` จากนั้นเลือก Department ของบริษัทจาก dropdown ในหน้าสร้างบัญชี
`POST /api/onboarding/join/check` ค้นหาบริษัทและรายชื่อ Department จากรหัสบริษัท
`POST /api/onboarding/join/register` ตรวจรหัสบริษัทและยืนยันว่า `departmentId` อยู่ในบริษัทนั้นก่อนบันทึกบัญชี
HR ต้องสร้าง Department อย่างน้อยหนึ่งรายการก่อนพนักงานจะสมัครได้
HR เปลี่ยนชื่อ Department ได้จากหน้า `/departments`; การลบจะถูกปฏิเสธหากมีพนักงาน ผลแบบสอบถาม หรือข้อมูลย้อนหลังที่อ้างถึงแผนกนั้น

### Executive เท่านั้น
`GET /api/dashboard/executive/summary`

### admin หรือ Executive
`POST /api/action-items` — Executive ได้ level `decision`, admin ได้ `full`

Knowledge Base (aggregate-only):
- `GET /api/knowledge-base` — index metadata ของบทความแต่ละรอบ
- `GET /api/knowledge-base/index` — Markdown index พร้อม wikilinks `[[YYYY-MM]]`
- `GET /api/knowledge-base/:periodId` — บทความ Markdown เต็มของรอบนั้น

admin เพิ่มเติม: `POST /api/knowledge-base/:periodId/compile?force=false` เพื่อ compile/recompile บทความด้วย LLM
ระบบจะลอง compile อัตโนมัติเมื่อปิดรอบสำรวจด้วย แต่ถ้า AI-Service ไม่พร้อม การปิดรอบยังสำเร็จและ HR สามารถ retry endpoint นี้ภายหลังได้

ทุก dashboard endpoint รับ `?period=<periodId>` (default = รอบล่าสุดขององค์กร)

---

## โครงสร้าง

```
cmd/server/          entrypoint + graceful shutdown
internal/
  config/            อ่าน env, fail fast ถ้าไม่มี JWT_SECRET
  models/            GORM models ตรงกับ schema ที่กำหนด + ตารางเสริม + Suppressible[T]/Localized
  dto/               request/response ทั้งหมด  ← มี test
  database/          connect, bootstrap (extension/enum/trigger), migrate, seed
  auth/              JWT access/refresh, voter hash
  middleware/        RequireAuth (คุณคือใคร) / RequireRole (เข้าอะไรได้)
  privacy/           n<5 + PII redaction  ← มี test
  analytics/         aggregation SQL ทั้งหมด (n<5 อยู่ใน HAVING) + alerts.go
  knowledgebase/     compile aggregate → persistent Markdown article + index/backlinks/source hash
  handlers/          auth, dashboard, survey, feed, periods, knowledgebase
  router/            ตารางสิทธิ์ทั้งหมดอยู่ที่นี่ไฟล์เดียว
```

### DTO layer

`internal/dto` คือ **wire format ทั้งหมด** — request ทุกอันที่รับ และ response ทุกอันที่ส่ง

ทำไมต้องแยก:
- **contract อยู่ที่เดียว มี compiler ตรวจ** ไม่ใช่ `fiber.Map` กระจายอยู่ใน handler ที่ไม่มี type
- **GORM model ไม่หลุดออก wire** เพิ่มคอลัมน์ใหม่จะไม่ publish ออกไปเงียบๆ (`PasswordHash` ไม่มี field ใน DTO เลย ไม่ใช่แค่ `json:"-"`)
- ชื่อ field ตรงกับ `Frontend/src/types/*.ts` แบบ 1:1 — **แก้ json tag ใน package นี้ = breaking change**

handler เหลือแค่ orchestration: parse → validate → query → map → return
validation ย้ายไปอยู่กับ request DTO (`Validate()` คืน list ของปัญหาทั้งหมด ไม่ใช่หยุดที่ error แรก)

**หมายเหตุเรื่อง router:** guard ผูกไว้ราย route ไม่ใช้ `Group("", mw)` เพราะ Fiber จะเอา middleware ของ group ที่ prefix ว่างไปใส่ให้ทุก route ที่ register หลังจากนั้นบน parent ทำให้ group ถัดไปติดสิทธิ์ของ group ก่อนหน้าโดยไม่รู้ตัว (เจอจริงตอน build — Executive โดน admin guard บล็อก)

---

## Dev

```bash
go test ./...          # dto + privacy packages มี test ครอบคลุม
go vet ./...
go build ./...
```

> ชุดงาน Knowledge Base ทดสอบด้วย `go test ./...`, `go vet ./...` และ PostgreSQL integration test บนฐานข้อมูลชั่วคราวแล้ว

Seed จะข้ามถ้ามีข้อมูลอยู่แล้ว (เช็คจากตาราง `organizations`) ล้างใหม่:

```bash
dropdb mtsense && createdb mtsense && go run ./cmd/server
```

ปิด seed ใน production: `SEED_ON_BOOT=false`

---

## ยังไม่ได้ทำ

- **ข้อมูล AI บน Dashboard บางส่วน** — sentiment และ categories ของคำตอบใหม่มาจาก
  AI-Service แล้ว ส่วน wordcloud แยกคำจากความคิดเห็นจริงผ่าน AI-Service และนับจำนวน
  คำตอบที่พบคำนั้น (แสดงเฉพาะคำที่อยู่ในอย่างน้อย 5 คำตอบ) ไม่ใช้ข้อมูลตัวอย่างใน
  `word_cloud_terms` อีกต่อไป ข้อความตัวอย่างในหน้า Topic ดึงจากคำตอบจริงที่ปกปิด
  ข้อมูลส่วนตัวแล้วและแสดงเฉพาะกลุ่มที่มีอย่างน้อย 5 คำตอบ ส่วน insight/urgent issues/sub-issues ยังมาจาก seed
  และคำตอบเก่าที่บันทึกก่อนเชื่อมต่อยังเป็นผลวิเคราะห์เดิม
- **สูตร burnout risk** — ตอนนี้ใช้ heuristic (สัดส่วนคนที่ให้คะแนนรวม ≤2) เพราะสเปกยังไม่ได้สรุปสูตร
- **`dashboard_metrics`/`position_scores`/`keywords_monthly`** — ตารางมีอยู่ตาม schema ที่กำหนด
  แต่ยังไม่มี batch job เขียนลงไป analytics ทั้งหมดยัง query สดเหมือนเดิม
- **Q&A บน Knowledge Base** — รอบนี้ทำ persistent LLM summary/wiki article, index และ backlinks แล้ว แต่ยังไม่มีหน้าถาม-ตอบหรือ retrieval orchestration; ให้ต่อยอดจาก `/api/knowledge-base` โดยไม่ต้องอ่านความคิดเห็นดิบใหม่
- **Cache** — dashboard ที่ aggregate หนักยัง query สดทุกครั้ง ตามสเปกควร cache รายวัน
- **Rate limiting** ที่ `/api/auth/login`
- **Multi-tenant login จริง** — org_id ผูกทุก query แล้ว แต่ยังไม่มีหน้าเลือกบริษัทก่อน login
  (ตอนนี้ resolve จาก organization แถวเดียวที่ seed ไว้)
- **Refresh token** rotate แล้ว (ใช้ซ้ำไม่ได้) แต่ยังไม่ได้ทำ reuse-detection ที่เพิกถอนทั้ง family
