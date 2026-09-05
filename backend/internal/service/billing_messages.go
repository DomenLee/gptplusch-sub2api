package service

// InsufficientBalanceClientMessage is the bilingual, actionable message used
// when a request is rejected because the user's wallet balance is exhausted.
// Keep the frontend route relative so the message remains valid behind a
// reverse proxy or a custom frontend domain.
const InsufficientBalanceClientMessage = "Insufficient account balance. Please visit /purchase to recharge, then retry. / 账户余额不足，请前往 /purchase 充值后重试。"
