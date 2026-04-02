enum VMCreationPollingReason {
    case observedListChange
    case createdVM
}

enum VMCreationPollingAction: Equatable {
    case keepCurrent
    case start
    case cancel
}

enum VMCreationPolling {
    static func action(
        hasRunningTask: Bool,
        observedCreatingVMNames: [String],
        reason: VMCreationPollingReason
    ) -> VMCreationPollingAction {
        switch reason {
        case .createdVM:
            return .start
        case .observedListChange:
            if observedCreatingVMNames.isEmpty {
                return hasRunningTask ? .cancel : .keepCurrent
            }
            return hasRunningTask ? .keepCurrent : .start
        }
    }
}
