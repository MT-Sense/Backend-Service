# MT-Sense — Backend Service

REST API สำหรับ [MT-Sense](../Web-Frontend) — ระบบเก็บ feedback พนักงานแบบไม่ระบุตัวตน

**Stack:** Go 1.25 · GoFiber v2 · GORM · PostgreSQL

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

ครั้งแรกจะ migrate + seed ข้อมูลตัวอย่างให้อัตโนมัติ (~1,200 responses ย้อนหลัง 6 เดือน)

### บัญชีทดลอง (มีเฉพาะตอน seed)

| Email | Password | Role |
|---|---|---|
| `hr@mtsense.local` | `password123` | HR |
| `exec@mtsense.local` | `password123` | Executive |
| `employee@mtsense.local` | `password123` | Employee |

---

## กฎความเป็นส่วนตัว — บังคับที่ backend ไม่ใช่แค่ซ่อนที่ UI

นี่คือเหตุผลที่ระบบนี้มีอยู่ ถ้าจะแก้โค้ดที่กระทบข้อใดข้อหนึ่ง **ต้องคุยกันก่อน**

### 1. คำตอบไม่ผูกกับตัวบุคคล

ตาราง `survey_responses` **ไม่มี foreign key ไปที่ `users`** เลย ใช้ `anonymous_token` ที่สุ่มใหม่ทุกครั้งแทน

เก็บ `department_id` กับ `tenure_bucket` ไว้เป็น *คุณลักษณะหยาบ* เพื่อให้แบ่งกลุ่มดูสถิติได้ ซึ่งปลอดภัยเพราะมีกฎ n<5 คุมอยู่ (ข้อ 2)

`user_id` ถูกใช้แค่ 2 อย่างตอน submit และไม่มีอันไหนถูกเขียนลง response:
- อ่านแผนก/อายุงานของผู้ตอบ
- เขียน `audit_submission_log` ว่า "ส่งแล้ว" (ไว้ส่งเมลเตือนคนที่ยังไม่ส่ง)

ทั้งหมดอยู่ใน transaction เดียว

### 2. n < 5 suppression — บังคับใน SQL

ทุก query ที่ `GROUP BY` แผนก/ทีม/อายุงาน มี `HAVING COUNT(DISTINCT survey_responses.id) >= 5`

**ด่านแรกอยู่ที่ database** — คะแนนของกลุ่มเล็กไม่ถูก select ออกมาตั้งแต่แรก จึงไม่มีสำเนาใน memory ให้เผลอส่งออกไป ส่วน `privacy.Suppress()` เป็นด่านที่สอง

ผลลัพธ์ที่ส่งออกไปตรงกับ type `Suppressible<T>` ของ frontend เป๊ะ:

```json
{"suppressed": true}
{"suppressed": false, "data": 3.9}
```

Seed จงใจใส่ทีม `innovation` ให้มีผู้ตอบแค่ 2 คน เพื่อให้ทดสอบเส้นทางนี้ได้จริง

### 3. PII redaction ก่อนเขียนลง DB

`privacy.Redact()` ตัดข้อมูลระบุตัวตนออก **ก่อน** persist — ข้อความดิบไม่เคยลงตาราง (ถ้ากรองตอนแสดงผล ของจริงจะยังนอนอยู่ใน DB ให้คนที่เข้าถึง query ได้อ่าน)

ครอบคลุม: อีเมล · เบอร์โทรไทย/สากล · เลขบัตรประชาชน 13 หลัก · รหัสพนักงาน · URL · @handle · คำนำหน้า+ชื่อ (ไทย/อังกฤษ)

> **ข้อจำกัดที่ต้องรู้:** ภาษาไทยเขียนติดกันไม่มีเว้นวรรค regex จึงไม่รู้ว่าชื่อจบตรงไหน — จำกัดไว้ 6 ตัวอักษร บางครั้งจะกินคำถัดไปนิดหน่อย **เลือกให้กินเกินดีกว่าปล่อยชื่อหลุด** ถ้าต้องการแม่นกว่านี้ต้องใส่ตัวตัดคำไทยหรือ NER

### 4. Feed ต้องผ่าน 2 ด่าน

`WHERE opted_in = true AND published = true` — เป็น **WHERE clause ไม่ใช่ filter ตอนแสดงผล** โพสต์ที่ยังไม่ผ่านทั้งสองด่านไม่ถูก select เลย

- ด่าน 1: ผู้ตอบติ๊กยินยอมตอนกรอกฟอร์ม
- ด่าน 2: HR review แล้วกด publish

API ปฏิเสธการ publish โพสต์ที่เจ้าของไม่เคยยินยอม (403)

### 5. Executive ไม่มีทางเห็นข้อความดิบ

`/dashboard/executive/summary` ไม่แตะคอลัมน์ข้อความเลย และ `/dashboard/hr/topics/:id` (endpoint เดียวที่คืนข้อความตัวอย่าง) ปิดด้วย `RequireRole(HR)`

### 6. Audit log แยกตาราง

`audit_submission_log` เก็บแค่ "ใครส่งแล้ว/ยังไม่ส่ง" ไม่มี key ร่วมกับ `survey_responses` — query ปกติ join ไม่ได้

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
| GET | `/api/settings/me/submissions` | ประวัติส่ง (ไม่มีเนื้อหาคำตอบ) |
| GET | `/api/topics` · `/api/departments` | |
| GET | `/api/surveys/:id` | |
| POST | `/api/surveys/:id/responses` | ส่งแบบไม่ระบุตัวตน |
| GET | `/api/feed` | `?sort=popular\|newest&tag=&limit=&offset=` |
| POST | `/api/feed/:id/vote` | `{"direction": 1 \| -1}` |
| GET | `/api/summaries` · `/api/action-items` | |

### HR เท่านั้น
| Method | Path |
|---|---|
| GET | `/api/dashboard/hr/kpi` · `/heatmap` · `/wordcloud` · `/insight` |
| GET | `/api/dashboard/hr/topics/:id` |
| GET/POST | `/api/forms` |
| PUT | `/api/forms/:id` |
| POST | `/api/forms/:id/publish` · `/api/forms/validate-question` |
| GET | `/api/feed/pending` |
| PATCH | `/api/feed/:id/moderate` |

### Executive เท่านั้น
`GET /api/dashboard/executive/summary`

### HR หรือ Executive
`POST /api/action-items` — Executive ได้ level `decision`, HR ได้ `full`

ทุก dashboard endpoint รับ `?month=YYYY-MM` (default = เดือนปัจจุบัน)

---

## โครงสร้าง

```
cmd/server/          entrypoint + graceful shutdown
internal/
  config/            อ่าน env, fail fast ถ้าไม่มี JWT_SECRET
  models/            GORM models + Suppressible[T] + Localized
  dto/               request/response ทั้งหมด  ← มี test
  database/          connect, migrate, seed
  auth/              JWT access/refresh, anonymous token, voter hash
  middleware/        RequireAuth (คุณคือใคร) / RequireRole (เข้าอะไรได้)
  privacy/           n<5 + PII redaction  ← มี test
  analytics/         aggregation SQL ทั้งหมด (n<5 อยู่ใน HAVING)
  handlers/          auth, dashboard, survey, feed, forms
  router/            ตารางสิทธิ์ทั้งหมดอยู่ที่นี่ไฟล์เดียว
```

### DTO layer

`internal/dto` คือ **wire format ทั้งหมด** — request ทุกอันที่รับ และ response ทุกอันที่ส่ง

ทำไมต้องแยก:
- **contract อยู่ที่เดียว มี compiler ตรวจ** ไม่ใช่ `fiber.Map` กระจายอยู่ใน handler ที่ไม่มี type
- **GORM model ไม่หลุดออก wire** เพิ่มคอลัมน์ใหม่จะไม่ publish ออกไปเงียบๆ (`PasswordHash` ไม่มี field ใน DTO เลย ไม่ใช่แค่ `json:"-"`)
- ชื่อ field ตรงกับ `Web-Frontend/src/types/*.ts` แบบ 1:1 — **แก้ json tag ใน package นี้ = breaking change**

handler เหลือแค่ orchestration: parse → validate → query → map → return
validation ย้ายไปอยู่กับ request DTO (`Validate()` คืน list ของปัญหาทั้งหมด ไม่ใช่หยุดที่ error แรก)

**หมายเหตุเรื่อง router:** guard ผูกไว้ราย route ไม่ใช้ `Group("", mw)` เพราะ Fiber จะเอา middleware ของ group ที่ prefix ว่างไปใส่ให้ทุก route ที่ register หลังจากนั้นบน parent ทำให้ group ถัดไปติดสิทธิ์ของ group ก่อนหน้าโดยไม่รู้ตัว (เจอจริงตอน build — Executive โดน HR guard บล็อก)

---

## Dev

```bash
go test ./...          # privacy package มี test ครอบคลุม redaction + n<5
go vet ./...
go build ./...
```

Seed จะข้ามถ้ามีข้อมูลอยู่แล้ว ล้างใหม่:

```bash
dropdb mtsense && createdb mtsense && go run ./cmd/server
```

ปิด seed ใน production: `SEED_ON_BOOT=false`

---

## ยังไม่ได้ทำ

- **AI pipeline ของจริง** — sentiment ตอนนี้เป็น keyword lookup, insight/urgent issues/wordcloud/sub-issues มาจาก seed ทุกจุดมีคอมเมนต์ `ponytail:` กำกับไว้ว่าจะเปลี่ยนตรงไหน คอลัมน์กับ aggregate ที่อ่านมันไม่ต้องแก้
- **สูตร burnout risk** — ตอนนี้ใช้ heuristic (สัดส่วนคนที่ให้คะแนน work ≤2) เพราะสเปกยังไม่ได้สรุปสูตร
- **Cache** — dashboard ที่ aggregate หนักยัง query สดทุกครั้ง ตามสเปกควร cache รายวัน
- **Rate limiting** ที่ `/api/auth/login`
- **ยังไม่ได้ต่อกับ frontend จริง** — frontend ยังใช้ mock อยู่ ต้องเขียน API client + ลบ dev role picker (`RoleSwitcherDev.vue`, role picker ใน `LoginView.vue`) แล้วต่อ `/api/auth/login` ของจริง
- **Refresh token** rotate แล้ว (ใช้ซ้ำไม่ได้) แต่ยังไม่ได้ทำ reuse-detection ที่เพิกถอนทั้ง family
