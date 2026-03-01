import SwiftUI

struct AuthView: View {
    var auth: AuthManager

    var body: some View {
        VStack(spacing: 32) {
            Spacer()

            VStack(spacing: 16) {
                Image("Exy")
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .frame(width: 120, height: 120)

                Text("exe.dev")
                    .font(.system(size: 36, weight: .bold, design: .monospaced))
                Text("Cloud development environments")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
            }

            Spacer()

            Button(action: { auth.signIn() }) {
                Text("Sign in to exe.dev")
                    .font(.headline)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 14)
            }
            .buttonStyle(.borderedProminent)
            .padding(.horizontal, 40)

            Spacer()
                .frame(height: 60)
        }
    }
}
