import type {
  AnswerVerdict,
  Choice,
  ExamSummary,
  Message,
  Question,
  QuestionDetail,
  Thread,
  ThreadWithMessages,
} from "../../db";

export function makeQuestion(overrides: Partial<Question> = {}): Question {
  return {
    id: 1,
    exam: "AWS Certified Test",
    topic: 1,
    question_number: 7,
    question_text: "Which AWS service is best for X?",
    question_text_ja: "X に最適な AWS サービスはどれか。",
    suggested_answer: "C",
    confirmed_answer: "C",
    explanation_ja: "正解は C である。",
    url: "https://example.com/q/1",
    comments: null,
    ...overrides,
  };
}

export function makeChoices(): Choice[] {
  return [
    { question_id: 1, label: "A", text: "Choice A", text_ja: "選択肢 A" },
    { question_id: 1, label: "B", text: "Choice B", text_ja: "選択肢 B" },
    { question_id: 1, label: "C", text: "Choice C", text_ja: "選択肢 C" },
    { question_id: 1, label: "D", text: "Choice D", text_ja: "選択肢 D" },
  ];
}

/** A settled verdict: the community agrees with the key. */
export function makeVerdict(overrides: Partial<AnswerVerdict> = {}): AnswerVerdict {
  return {
    status: "settled",
    accepted: ["C"],
    community: [
      { label: "C", votes: 8, pct: 80 },
      { label: "B", votes: 2, pct: 20 },
    ],
    total_votes: 10,
    rationale: "コミュニティ多数派 C (8/10 = 80%) が正解キーと一致",
    ...overrides,
  };
}

export function makeQuestionDetail(
  overrides: Partial<QuestionDetail> = {}
): QuestionDetail {
  return {
    q: makeQuestion(),
    choices: makeChoices(),
    attempts: [],
    verdict: makeVerdict(),
    prevId: null,
    nextId: 2,
    ...overrides,
  };
}

// Fixture ids are 26-char Crockford base32 strings (UUIDv7 BLOB
// encoded). The deterministic literals below are NOT real UUIDv7s
// — they're hand-picked so tests have stable, recognizable values
// instead of random output.
const THREAD_ID_FIXTURE = "01HZX5K2ABCDEFGHJKMNPQRSTV";
const MESSAGE_ID_FIXTURE = "01HZX5K2WXYZ0123456789ABCD";

// fixtureId returns a stable 26-char Crockford-base32 id derived from
// a small integer, so tests that previously used `id: 1` etc. still
// have a unique, reproducible string they can plug into assertions.
// First char stays in 0–7 (the valid leading-byte range); the low
// bits encode `n` in big-endian base32.
const ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
export function fixtureId(n: number): string {
  let s = "";
  let x = n;
  while (x > 0) {
    s = ALPHABET[x & 31] + s;
    x = Math.floor(x / 32);
  }
  if (s === "") s = "0";
  return s.padStart(26, "0");
}

export function makeThread(
  overrides: Partial<Thread> = {}
): Thread {
  return {
    id: THREAD_ID_FIXTURE,
    question_id: 1,
    status: "open",
    created_at: "2026-04-30 10:00:00",
    closed_at: null,
    agent_session_id: null,
    ...overrides,
  };
}

export function makeMessage(overrides: Partial<Message> = {}): Message {
  return {
    id: MESSAGE_ID_FIXTURE,
    thread_id: THREAD_ID_FIXTURE,
    role: "user",
    author: "web",
    content: "なぜ C が正解なのか",
    reason_code: null,
    citations: null,
    translation_diff: null,
    created_at: "2026-04-30 10:00:00",
    ...overrides,
  };
}

export function makeThreadWithMessages(
  thread: Partial<Thread> = {},
  messages: Message[] = [makeMessage()]
): ThreadWithMessages {
  return { ...makeThread(thread), messages };
}

export function makeExamSummary(
  overrides: Partial<ExamSummary> = {}
): ExamSummary {
  return {
    slug: "soa-c03",
    name: "AWS Certified CloudOps Engineer SOA-C03",
    total: 100,
    translated: 80,
    answered: 50,
    correct: 35,
    open_threads: 2,
    awaiting_agent: 1,
    ...overrides,
  };
}
