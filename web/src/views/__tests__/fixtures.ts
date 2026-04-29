import type {
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

export function makeQuestionDetail(
  overrides: Partial<QuestionDetail> = {}
): QuestionDetail {
  return {
    q: makeQuestion(),
    choices: makeChoices(),
    attempts: [],
    prevId: null,
    nextId: 2,
    ...overrides,
  };
}

export function makeThread(
  overrides: Partial<Thread> = {}
): Thread {
  return {
    id: 42,
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
    id: 100,
    thread_id: 42,
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
